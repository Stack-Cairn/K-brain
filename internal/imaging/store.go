package imaging

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gofrs/flock"
)

const CodeStoreLocked = "IMAGE_STORE_LOCKED"
const CodeStoreCorrupt = "IMAGE_STORE_CORRUPT"
const CodeProjectBusy = "IMAGE_PROJECT_BUSY"

var errStoreClosed = errors.New("IMAGE_STORE_CLOSED")

type storeScopeKey struct{}

// WithScope binds authenticated server identity. Never fill Scope from request JSON.
func WithScope(ctx context.Context, scope Scope) context.Context {
	return context.WithValue(ctx, storeScopeKey{}, scope)
}

type Store struct {
	mu                   sync.RWMutex
	dir                  string
	lock                 *flock.Flock
	wal                  *os.File
	data                 storeData
	seq                  uint64
	checkpointSeq        uint64
	walBytes             int64
	cursorKey            []byte
	closed               bool
	poisoned             error
	maintenanceErr       error
	now                  func() time.Time
	fault                func(string) error // Test-only crash boundaries; never called while recovering.
	snapshotTransactions uint64
	snapshotBytes        int64
	reasons              map[string]jobReasons
}

func OpenStore(dir string) (*Store, error) {
	if dir == "" {
		return nil, invalid("empty store directory")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	lock := flock.New(filepath.Join(dir, "writer.lock"))
	ok, err := lock.TryLock()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%s: imaging directory already has a writer", CodeStoreLocked)
	}
	s := &Store{dir: dir, lock: lock, data: newStoreData(), now: time.Now, snapshotTransactions: 1000, snapshotBytes: 64 << 20}
	if err = s.recover(); err != nil {
		if s.wal != nil {
			s.wal.Close()
		}
		lock.Unlock()
		return nil, err
	}
	s.restoreReasons()
	return s, nil
}
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return errors.Join(s.wal.Close(), s.lock.Unlock())
}
func (s *Store) hit(point string) error {
	if s.fault != nil {
		return s.fault(point)
	}
	return nil
}

// MaintenanceError reports a failed automatic checkpoint. Its preceding transaction
// is already durable; Update therefore still succeeds. Checkpoint can be retried.
func (s *Store) MaintenanceError() error { s.mu.RLock(); defer s.mu.RUnlock(); return s.maintenanceErr }
func (s *Store) Update(ctx context.Context, fn func(*Transaction) error) error {
	scope, ok := ctx.Value(storeScopeKey{}).(Scope)
	if !ok || scope.OwnerID == "" || scope.BackendID == "" {
		return invalid("trusted scope required")
	}
	if fn == nil {
		return invalid("transaction callback required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errStoreClosed
	}
	if s.poisoned != nil {
		return s.poisoned
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	tx := &Transaction{scope: scope, data: cloneStoreValue(s.data), now: s.now().UTC(), active: true, changedJobs: map[string]bool{}}
	defer func() { tx.active = false }()
	if err := fn(tx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := tx.validate(); err != nil {
		return err
	}
	if err := validateStoreData(tx.data); err != nil {
		return invalid("transaction violates durable references: " + err.Error())
	}
	if len(tx.mutations) == 0 {
		return nil
	}
	// Serialize a detached graph; neither callback-retained pointers nor subsequent
	// caller mutation can change the published state or the committed log payload.
	next := cloneStoreValue(tx.data)
	if err := s.appendTransaction(tx.mutations); err != nil {
		return err
	}
	s.data = next
	s.recordReasons(tx.changedJobs)
	if s.seq-s.checkpointSeq >= s.snapshotTransactions || s.walBytes >= s.snapshotBytes {
		s.maintenanceErr = s.checkpoint()
	}
	return nil
}

type Transaction struct {
	scope       Scope
	data        storeData
	now         time.Time
	mutations   []storeMutation
	active      bool
	changedJobs map[string]bool
}

func (tx *Transaction) check() error {
	if !tx.active {
		return errors.New("IMAGE_TRANSACTION_CLOSED")
	}
	return nil
}
func (tx *Transaction) ReadProject(scope Scope, id string) (ImageProject, error) {
	if err := tx.check(); err != nil {
		return ImageProject{}, err
	}
	if scope != tx.scope {
		return ImageProject{}, notFound()
	}
	return readProject(tx.data, scope, id)
}
func (tx *Transaction) ReadJob(scope Scope, id string) (ImageJob, error) {
	if err := tx.check(); err != nil {
		return ImageJob{}, err
	}
	if scope != tx.scope {
		return ImageJob{}, notFound()
	}
	return readJob(tx.data, scope, id)
}
func (tx *Transaction) ReadAsset(scope Scope, id string) (ImageAsset, error) {
	if err := tx.check(); err != nil {
		return ImageAsset{}, err
	}
	if scope != tx.scope {
		return ImageAsset{}, notFound()
	}
	return readAsset(tx.data, scope, id)
}
func notFound() error { return &ImageError{Code: CodeNotFound} }
func conflict() error { return &ImageError{Code: CodeRevisionConflict} }
func readProject(d storeData, scope Scope, id string) (ImageProject, error) {
	r, ok := d.Projects[id]
	if !ok || !r.Scope.matches(scope) || r.DeletedAt != nil {
		return ImageProject{}, notFound()
	}
	return cloneStoreValue(r.Value), nil
}
func readJob(d storeData, scope Scope, id string) (ImageJob, error) {
	r, ok := d.Jobs[id]
	if !ok || !r.Scope.matches(scope) || r.DeletedAt != nil {
		return ImageJob{}, notFound()
	}
	return cloneStoreValue(PublicJob(r.Value)), nil
}
func readAsset(d storeData, scope Scope, id string) (ImageAsset, error) {
	r, ok := d.Assets[id]
	if !ok || !r.Scope.matches(scope) {
		return ImageAsset{}, notFound()
	}
	return cloneStoreValue(PublicAsset(r.Value)), nil
}
func (s *Store) ReadProject(scope Scope, id string) (ImageProject, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return ImageProject{}, errStoreClosed
	}
	return readProject(s.data, scope, id)
}
func (s *Store) ReadJob(scope Scope, id string) (ImageJob, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return ImageJob{}, errStoreClosed
	}
	return readJob(s.data, scope, id)
}
func (s *Store) ReadAsset(scope Scope, id string) (ImageAsset, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return ImageAsset{}, errStoreClosed
	}
	return readAsset(s.data, scope, id)
}
func (tx *Transaction) PutProject(v ImageProject) error {
	if err := tx.check(); err != nil {
		return err
	}
	if v.ID == "" || v.Revision == 0 || v.Revision > MaxSafeInteger {
		return invalid("project identity/revision required")
	}
	if old, ok := tx.data.Projects[v.ID]; ok {
		if !old.Scope.matches(tx.scope) || old.DeletedAt != nil {
			return notFound()
		}
		if v.Revision != old.Value.Revision+1 || !v.CreatedAt.Equal(old.Value.CreatedAt) {
			return conflict()
		}
	}
	r := projectRecord{Scope: owner(tx.scope), Value: cloneStoreValue(v)}
	tx.data.Projects[v.ID] = r
	tx.mutations = append(tx.mutations, storeMutation{Kind: "project", Project: &r})
	return nil
}
func (tx *Transaction) PutJob(v ImageJob) error {
	if err := tx.check(); err != nil {
		return err
	}
	if v.ID == "" || v.Revision == 0 || v.Revision > MaxSafeInteger {
		return invalid("job identity/revision required")
	}
	if _, err := readProject(tx.data, tx.scope, v.ProjectID); err != nil {
		return err
	}
	r := jobRecord{Scope: owner(tx.scope), FirstAvailableSeq: 1}
	if old, ok := tx.data.Jobs[v.ID]; ok {
		if !old.Scope.matches(tx.scope) || old.DeletedAt != nil {
			return notFound()
		}
		if v.Revision != old.Value.Revision+1 || v.ProjectID != old.Value.ProjectID || !v.CreatedAt.Equal(old.Value.CreatedAt) {
			return conflict()
		}
		r = old
	}
	// Existing references remain valid historical metadata after content is lost.
	// New references must be checked under the same Store lock as GC's sweep.
	existingRefs := map[string]bool{}
	for _, id := range jobAssetIDs(r.Value) {
		existingRefs[id] = true
	}
	for _, id := range jobAssetIDs(v) {
		asset, err := readAsset(tx.data, tx.scope, id)
		if err != nil {
			return err
		}
		if !asset.Available && !existingRefs[id] {
			return notFound()
		}
	}
	existingInputs := map[string]bool{}
	for _, input := range r.Value.Request.Inputs {
		existingInputs[input.AssetID] = true
	}
	for _, input := range v.Request.Inputs {
		if !existingInputs[input.AssetID] && !tx.data.Assets[input.AssetID].Value.Available {
			return notFound()
		}
	}
	if v.Request.ParentAssetID != "" {
		if v.Request.ParentAssetID != r.Value.Request.ParentAssetID && !tx.data.Assets[v.Request.ParentAssetID].Value.Available {
			return notFound()
		}
		found := false
		for _, input := range v.Request.Inputs {
			if input.AssetID == v.Request.ParentAssetID {
				found = true
			}
		}
		if !found {
			return invalid("parent asset must be a request input")
		}
	}
	r.Value = cloneStoreValue(PublicJob(v))
	tx.data.Jobs[v.ID] = r
	tx.changedJobs[v.ID] = true
	tx.mutations = append(tx.mutations, storeMutation{Kind: "job", Job: &r})
	return nil
}
func jobAssetIDs(v ImageJob) []string {
	ids := append([]string{}, v.AssetIDs...)
	for _, i := range v.Request.Inputs {
		ids = append(ids, i.AssetID)
	}
	if v.Request.ParentAssetID != "" {
		ids = append(ids, v.Request.ParentAssetID)
	}
	for _, i := range v.Items {
		ids = append(ids, i.OutputAssetIDs...)
	}
	return ids
}
func (tx *Transaction) PutAsset(v ImageAsset) error {
	if err := tx.check(); err != nil {
		return err
	}
	if v.ID == "" {
		return invalid("asset ID required")
	}
	existingParents := map[string]bool{}
	if old, ok := tx.data.Assets[v.ID]; ok {
		if !old.Scope.matches(tx.scope) {
			return notFound()
		}
		if old.Value.Hash != v.Hash || !old.Value.CreatedAt.Equal(v.CreatedAt) {
			return conflict()
		}
		for _, id := range old.Value.ParentAssetIDs {
			existingParents[id] = true
		}
	}
	for _, id := range v.ParentAssetIDs {
		if id == v.ID {
			return invalid("cyclic asset parent")
		}
		parent, err := readAsset(tx.data, tx.scope, id)
		if err != nil {
			return err
		}
		// Reclaimed parents still describe existing ancestry, but cannot gain
		// a new child after GC has removed their content.
		if !parent.Available && !existingParents[id] {
			return notFound()
		}
		seen := map[string]bool{}
		var visit func(string) bool
		visit = func(id string) bool {
			if id == v.ID {
				return true
			}
			if seen[id] {
				return false
			}
			seen[id] = true
			for _, p := range tx.data.Assets[id].Value.ParentAssetIDs {
				if visit(p) {
					return true
				}
			}
			return false
		}
		if visit(id) {
			return invalid("cyclic asset parent")
		}
	}
	if v.JobID != "" {
		j, err := readJob(tx.data, tx.scope, v.JobID)
		if err != nil {
			return err
		}
		for _, p := range v.ParentAssetIDs {
			found := false
			for _, i := range j.Request.Inputs {
				if i.AssetID == p {
					found = true
				}
			}
			if !found {
				return invalid("output parent must be a job input")
			}
		}
	}
	r := assetRecord{Scope: owner(tx.scope), Value: cloneStoreValue(PublicAsset(v))}
	tx.data.Assets[v.ID] = r
	tx.mutations = append(tx.mutations, storeMutation{Kind: "asset", Asset: &r})
	return nil
}
func (tx *Transaction) AppendEvent(v ImageEvent) error {
	if err := tx.check(); err != nil {
		return err
	}
	j, err := readJob(tx.data, tx.scope, v.JobID)
	if err != nil {
		return err
	}
	r := tx.data.Jobs[v.JobID]
	if r.LastEventSeq >= MaxSafeInteger {
		return invalid("event sequence exhausted")
	}
	if v.Seq != 0 && v.Seq != r.LastEventSeq+1 {
		return invalid("event sequence mismatch")
	}
	if v.Revision != 0 && v.Revision != j.Revision {
		return conflict()
	}
	switch v.Type {
	case EventJobAccepted:
		if r.LastEventSeq != 0 {
			return invalid("duplicate accepted event")
		}
	case EventJobStateChanged, EventItemStateChanged, EventAssetReady, EventJobFinished:
	default:
		return invalid("unknown event type")
	}
	if v.Type == EventJobFinished && v.Data.Status == "" {
		return invalid("finished event requires status")
	}
	if v.Data.Status != "" && v.Data.Status != j.Status && v.Type != EventItemStateChanged {
		return invalid("event state mismatch")
	}
	if v.Data.Asset != nil {
		a, err := readAsset(tx.data, tx.scope, v.Data.Asset.ID)
		if err != nil {
			return err
		}
		v.Data.Asset = &a
	}
	v.Seq = r.LastEventSeq + 1
	v.Revision = j.Revision
	if v.Timestamp.IsZero() {
		v.Timestamp = tx.now
	}
	v = cloneStoreValue(PublicEvent(v))
	r.LastEventSeq = v.Seq
	tx.data.Jobs[v.JobID] = r
	tx.data.Events[v.JobID] = append(tx.data.Events[v.JobID], v)
	tx.mutations = append(tx.mutations, storeMutation{Kind: "job", Job: &r}, storeMutation{Kind: "event", Event: &v})
	return nil
}
func (tx *Transaction) validate() error {
	for id := range tx.changedJobs {
		r := tx.data.Jobs[id]
		if r.DeletedAt != nil {
			continue
		}
		events := tx.data.Events[id]
		if len(events) == 0 || events[len(events)-1].Revision != r.Value.Revision {
			return invalid("job and matching event must commit together")
		}
	}
	return nil
}
func (tx *Transaction) GetIdempotency(scope Scope, key string) (IdempotencyRecord, bool) {
	if !tx.active || scope != tx.scope {
		return IdempotencyRecord{}, false
	}
	return getIdempotency(tx.data, scope, key, tx.now)
}
func getIdempotency(d storeData, scope Scope, key string, now time.Time) (IdempotencyRecord, bool) {
	r, ok := d.Idempotency[idemIndex(scope, key)]
	if !ok || !r.Scope.matches(scope) || (!r.Value.ExpiresAt.IsZero() && !now.Before(r.Value.ExpiresAt)) {
		return IdempotencyRecord{}, false
	}
	return r.Value, true
}
func (s *Store) GetIdempotency(scope Scope, key string) (IdempotencyRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return IdempotencyRecord{}, false
	}
	return getIdempotency(s.data, scope, key, s.now())
}
func (tx *Transaction) PutIdempotency(scope Scope, key, hash, jobID string) error {
	if err := tx.check(); err != nil {
		return err
	}
	if scope != tx.scope {
		return notFound()
	}
	if key == "" || hash == "" {
		return invalid("idempotency key/hash required")
	}
	if old, ok := tx.GetIdempotency(scope, key); ok {
		if old.Hash != hash || old.JobID != jobID {
			return &ImageError{Code: CodeIdempotencyConflict}
		}
		return nil
	}
	if _, err := readJob(tx.data, scope, jobID); err != nil {
		return err
	}
	r := idempotencyEntry{Scope: owner(scope), Key: key, Value: IdempotencyRecord{Hash: hash, JobID: jobID}}
	tx.data.Idempotency[idemIndex(scope, key)] = r
	tx.mutations = append(tx.mutations, storeMutation{Kind: "idempotency", Idempotency: &r})
	return nil
}
func (tx *Transaction) PutExecution(v ExecutionRecord) error {
	if err := tx.check(); err != nil {
		return err
	}
	j, err := readJob(tx.data, tx.scope, v.JobID)
	if err != nil {
		return err
	}
	for id := range v.Items {
		found := false
		for _, i := range j.Items {
			if i.ID == id {
				found = true
			}
		}
		if !found {
			return invalid("execution item does not belong to job")
		}
	}
	d := executionToDisk(tx.scope, v)
	tx.data.Executions[v.JobID] = d
	tx.mutations = append(tx.mutations, storeMutation{Kind: "execution", Execution: &d})
	return nil
}
func readExecution(d storeData, scope Scope, id string) (ExecutionRecord, error) {
	if _, err := readJob(d, scope, id); err != nil {
		return ExecutionRecord{}, err
	}
	r, ok := d.Executions[id]
	if !ok || !r.Scope.matches(scope) {
		return ExecutionRecord{}, notFound()
	}
	return executionFromDisk(r), nil
}
func (tx *Transaction) ReadExecution(scope Scope, id string) (ExecutionRecord, error) {
	if err := tx.check(); err != nil {
		return ExecutionRecord{}, err
	}
	if scope != tx.scope {
		return ExecutionRecord{}, notFound()
	}
	return readExecution(tx.data, scope, id)
}
func (s *Store) ReadExecution(scope Scope, id string) (ExecutionRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return ExecutionRecord{}, errStoreClosed
	}
	return readExecution(s.data, scope, id)
}

type EventPosition struct {
	FirstAvailableSeq uint64 `json:"firstAvailableSeq"`
	LastEventSeq      uint64 `json:"lastEventSeq"`
}

// ReadJobSnapshot reads the job and event cursor in a single consistent view.
func (s *Store) ReadJobSnapshot(scope Scope, id string) (ImageJob, EventPosition, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return ImageJob{}, EventPosition{}, errStoreClosed
	}
	j, err := readJob(s.data, scope, id)
	if err != nil {
		return j, EventPosition{}, err
	}
	r := s.data.Jobs[id]
	return j, EventPosition{r.FirstAvailableSeq, r.LastEventSeq}, nil
}
func (s *Store) ReadEvents(ctx context.Context, scope Scope, id string, after uint64, limit int) ([]ImageEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 100 {
		return nil, invalid("limit must be 1..100")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, errStoreClosed
	}
	if _, err := readJob(s.data, scope, id); err != nil {
		return nil, err
	}
	r := s.data.Jobs[id]
	if after > r.LastEventSeq || after < r.FirstAvailableSeq-1 {
		first, last := r.FirstAvailableSeq, r.LastEventSeq
		return nil, &ImageError{Code: CodeEventGap, Details: &ErrorDetails{FirstAvailableSeq: &first, LastEventSeq: &last}}
	}
	out := []ImageEvent{}
	for _, e := range s.data.Events[id] {
		if e.Seq > after {
			out = append(out, cloneStoreValue(PublicEvent(e)))
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}

// PruneEvents only removes the contiguous prefix older than 30 days.
func (tx *Transaction) PruneEvents(id string) error {
	if err := tx.check(); err != nil {
		return err
	}
	if _, err := readJob(tx.data, tx.scope, id); err != nil {
		return err
	}
	events := tx.data.Events[id]
	n := 0
	for n < len(events) && events[n].Timestamp.Before(tx.now.Add(-30*24*time.Hour)) {
		n++
	}
	if n == 0 {
		return nil
	}
	r := tx.data.Jobs[id]
	r.FirstAvailableSeq = events[n-1].Seq + 1
	tx.data.Jobs[id] = r
	tx.data.Events[id] = append([]ImageEvent{}, events[n:]...)
	p := eventPrune{JobID: id, BeforeSeq: r.FirstAvailableSeq}
	tx.mutations = append(tx.mutations, storeMutation{Kind: "job", Job: &r}, storeMutation{Kind: "pruneEvents", Prune: &p})
	return nil
}

// AssetReferenceCounts is derived from durable live jobs and asset ancestry; it
// deliberately retains parent references across project deletion for safe GC.
func (s *Store) AssetReferenceCounts(scope Scope) map[string]int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := map[string]int{}
	for _, j := range s.data.Jobs {
		if j.Scope.matches(scope) && j.DeletedAt == nil {
			for _, id := range jobAssetIDs(j.Value) {
				out[id]++
			}
		}
	}
	for _, a := range s.data.Assets {
		if a.Scope.matches(scope) {
			for _, id := range a.Value.ParentAssetIDs {
				out[id]++
			}
		}
	}
	return out
}
