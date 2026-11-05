package imaging

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
)

type eventPrune struct {
	JobID     string `json:"jobId"`
	BeforeSeq uint64 `json:"beforeSeq"`
}

// Every WAL mutation is a closed, typed union. Unknown versions/kinds fail closed.
type storeMutation struct {
	Kind        string            `json:"kind"`
	Project     *projectRecord    `json:"project,omitempty"`
	Job         *jobRecord        `json:"job,omitempty"`
	Asset       *assetRecord      `json:"asset,omitempty"`
	Event       *ImageEvent       `json:"event,omitempty"`
	Idempotency *idempotencyEntry `json:"idempotency,omitempty"`
	Execution   *executionDisk    `json:"execution,omitempty"`
	Prune       *eventPrune       `json:"prune,omitempty"`
}
type transactionPayload struct {
	Version   int             `json:"version"`
	Mutations []storeMutation `json:"mutations"`
}
type checkedRecord struct {
	TxSeq   uint64          `json:"txSeq"`
	Payload json.RawMessage `json:"payload"`
	SHA256  string          `json:"sha256"`
}

func recordChecksum(seq uint64, payload []byte) string {
	h := sha256.New()
	h.Write([]byte(strconv.FormatUint(seq, 10)))
	h.Write([]byte{'\n'})
	h.Write(payload)
	return hex.EncodeToString(h.Sum(nil))
}
func encodeRecord(seq uint64, payload any) ([]byte, error) {
	p, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if p, err = stripErrorReasons(p); err != nil {
		return nil, err
	}
	b, err := json.Marshal(checkedRecord{seq, p, recordChecksum(seq, p)})
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
func decodeStrict(b []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("trailing JSON data")
	}
	return nil
}
func decodeRecord(b []byte) (checkedRecord, error) {
	var r checkedRecord
	if err := decodeStrict(b, &r); err != nil {
		return r, err
	}
	if r.Payload == nil || r.SHA256 != recordChecksum(r.TxSeq, r.Payload) {
		return r, errors.New("checksum mismatch")
	}
	return r, nil
}
func corrupt(where string, err error) error {
	return fmt.Errorf("%s: %s: %w", CodeStoreCorrupt, where, err)
}
func (s *Store) appendTransaction(m []storeMutation) error {
	if s.seq == ^uint64(0) {
		return errors.New("transaction sequence exhausted")
	}
	b, err := encodeRecord(s.seq+1, transactionPayload{Version: 1, Mutations: m})
	if err != nil {
		return err
	}
	start, err := s.wal.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	if err = s.hit("wal.before_append"); err != nil {
		return err
	}
	n, err := s.wal.Write(b)
	if err == nil && n != len(b) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = s.hit("wal.after_append")
	}
	if err == nil {
		err = s.hit("wal.before_sync")
	}
	if err == nil {
		err = s.wal.Sync()
	}
	if err == nil {
		err = s.hit("wal.after_sync")
	}
	if err != nil {
		// Undo a failed append before accepting another writer operation. If rollback
		// cannot be made durable, stop writing and report an explicit uncertain store.
		rollback := s.wal.Truncate(start)
		if rollback == nil {
			rollback = s.wal.Sync()
		}
		if rollback != nil {
			s.poisoned = fmt.Errorf("IMAGE_STORE_UNCERTAIN: WAL rollback failed: %w", rollback)
			return errors.Join(err, s.poisoned)
		}
		return err
	}
	s.seq++
	s.walBytes = start + int64(len(b))
	return nil
}
func (s *Store) replayWAL() error {
	if _, err := s.wal.Seek(0, io.SeekStart); err != nil {
		return err
	}
	reader := bufio.NewReader(s.wal)
	var offset int64
	var previous uint64
	seen := false
	for {
		line, err := reader.ReadBytes('\n')
		if err == io.EOF {
			if len(line) > 0 {
				if err = s.wal.Truncate(offset); err != nil {
					return err
				}
				if err = s.wal.Sync(); err != nil {
					return err
				}
			}
			break
		}
		if err != nil {
			return err
		}
		r, err := decodeRecord(line)
		if err != nil {
			return corrupt(fmt.Sprintf("WAL byte %d (complete line retained)", offset), err)
		}
		if r.TxSeq == 0 || (seen && r.TxSeq != previous+1) || (!seen && r.TxSeq > s.checkpointSeq+1) {
			return corrupt(fmt.Sprintf("WAL byte %d", offset), errors.New("transaction sequence gap"))
		}
		previous = r.TxSeq
		seen = true
		var p transactionPayload
		if err = decodeStrict(r.Payload, &p); err != nil {
			return corrupt("WAL payload", err)
		}
		if p.Version != 1 || len(p.Mutations) == 0 {
			return corrupt("WAL payload", errors.New("unsupported version or empty transaction"))
		}
		for _, m := range p.Mutations {
			if err = validateMutation(m); err != nil {
				return corrupt("WAL mutation", err)
			}
		}
		if r.TxSeq > s.checkpointSeq {
			if r.TxSeq != s.seq+1 {
				return corrupt("WAL", errors.New("transaction sequence gap"))
			}
			for _, m := range p.Mutations {
				if err = applyMutation(&s.data, m); err != nil {
					return corrupt("WAL mutation", err)
				}
			}
			if err = validateStoreData(s.data); err != nil {
				return corrupt("WAL state", err)
			}
			s.seq = r.TxSeq
		}
		offset += int64(len(line))
	}
	s.walBytes = offset
	_, err := s.wal.Seek(0, io.SeekEnd)
	return err
}
func validateMutation(m storeMutation) error {
	n := 0
	for _, present := range []bool{m.Project != nil, m.Job != nil, m.Asset != nil, m.Event != nil, m.Idempotency != nil, m.Execution != nil, m.Prune != nil} {
		if present {
			n++
		}
	}
	if n != 1 {
		return errors.New("mutation must have exactly one typed value")
	}
	switch m.Kind {
	case "project":
		if m.Project != nil {
			return nil
		}
	case "job":
		if m.Job != nil {
			return nil
		}
	case "asset":
		if m.Asset != nil {
			return nil
		}
	case "event":
		if m.Event != nil {
			return nil
		}
	case "idempotency":
		if m.Idempotency != nil {
			return nil
		}
	case "execution":
		if m.Execution != nil {
			return nil
		}
	case "pruneEvents":
		if m.Prune != nil {
			return nil
		}
	}
	return errors.New("unknown or mismatched mutation kind")
}
func applyMutation(d *storeData, m storeMutation) error {
	if err := validateMutation(m); err != nil {
		return err
	}
	switch m.Kind {
	case "project":
		if old, ok := d.Projects[m.Project.Value.ID]; ok && old.Scope != m.Project.Scope {
			return errors.New("project ownership changed")
		}
		d.Projects[m.Project.Value.ID] = *m.Project
	case "job":
		if old, ok := d.Jobs[m.Job.Value.ID]; ok && old.Scope != m.Job.Scope {
			return errors.New("job ownership changed")
		}
		d.Jobs[m.Job.Value.ID] = *m.Job
	case "asset":
		if old, ok := d.Assets[m.Asset.Value.ID]; ok && old.Scope != m.Asset.Scope {
			return errors.New("asset ownership changed")
		}
		d.Assets[m.Asset.Value.ID] = *m.Asset
	case "event":
		events := d.Events[m.Event.JobID]
		if len(events) > 0 && m.Event.Seq != events[len(events)-1].Seq+1 {
			return errors.New("event sequence gap")
		}
		d.Events[m.Event.JobID] = append(events, *m.Event)
	case "idempotency":
		r := m.Idempotency
		d.Idempotency[idemIndex(Scope{OwnerID: r.Scope.OwnerID, BackendID: r.Scope.BackendID}, r.Key)] = *r
	case "execution":
		d.Executions[m.Execution.JobID] = *m.Execution
	case "pruneEvents":
		events := d.Events[m.Prune.JobID]
		n := 0
		for n < len(events) && events[n].Seq < m.Prune.BeforeSeq {
			n++
		}
		d.Events[m.Prune.JobID] = append([]ImageEvent{}, events[n:]...)
	}
	return nil
}
func validOwner(s recordScope) bool { return s.OwnerID != "" && s.BackendID != "" }
func validateStoreData(d storeData) error {
	if d.Projects == nil || d.Jobs == nil || d.Assets == nil || d.Events == nil || d.Idempotency == nil || d.Executions == nil {
		return errors.New("missing record collections")
	}
	for id, r := range d.Projects {
		if id == "" || id != r.Value.ID || !validOwner(r.Scope) || r.Value.Revision == 0 || r.Value.Revision > MaxSafeInteger {
			return errors.New("invalid project record")
		}
	}
	for id, r := range d.Jobs {
		if id == "" || id != r.Value.ID || !validOwner(r.Scope) || r.Value.Revision == 0 || r.Value.Revision > MaxSafeInteger || r.LastEventSeq > MaxSafeInteger || r.FirstAvailableSeq == 0 || r.FirstAvailableSeq > r.LastEventSeq+1 {
			return errors.New("invalid job record")
		}
		p, ok := d.Projects[r.Value.ProjectID]
		if !ok || p.Scope != r.Scope || (p.DeletedAt != nil && r.DeletedAt == nil) {
			return errors.New("invalid project reference")
		}
		for _, aid := range jobAssetIDs(r.Value) {
			a, ok := d.Assets[aid]
			if !ok || a.Scope != r.Scope {
				return errors.New("invalid job asset reference")
			}
		}
		events := d.Events[id]
		expected := r.FirstAvailableSeq
		var revision uint64
		for _, e := range events {
			if e.JobID != id || e.Seq != expected || e.Revision == 0 || e.Revision > r.Value.Revision || e.Revision < revision {
				return errors.New("invalid event sequence/revision")
			}
			switch e.Type {
			case EventJobAccepted, EventJobStateChanged, EventItemStateChanged, EventAssetReady, EventJobFinished:
			default:
				return errors.New("unknown event type")
			}
			if e.Data.Asset != nil {
				a, ok := d.Assets[e.Data.Asset.ID]
				if !ok || a.Scope != r.Scope {
					return errors.New("invalid event asset reference")
				}
			}
			expected++
			revision = e.Revision
		}
		if expected != r.LastEventSeq+1 {
			return errors.New("event range mismatch")
		}
		if len(events) > 0 && revision != r.Value.Revision {
			return errors.New("job/event revision mismatch")
		}
	}
	for id := range d.Events {
		if _, ok := d.Jobs[id]; !ok {
			return errors.New("orphan event collection")
		}
	}
	for id, r := range d.Assets {
		if id == "" || id != r.Value.ID || !validOwner(r.Scope) {
			return errors.New("invalid asset record")
		}
		for _, p := range r.Value.ParentAssetIDs {
			a, ok := d.Assets[p]
			if !ok || a.Scope != r.Scope {
				return errors.New("invalid parent asset reference")
			}
		}
		if r.Value.JobID != "" {
			j, ok := d.Jobs[r.Value.JobID]
			if !ok || j.Scope != r.Scope {
				return errors.New("invalid asset job reference")
			}
		}
	}
	colors := map[string]int{}
	var visit func(string) bool
	visit = func(id string) bool {
		if colors[id] == 1 {
			return false
		}
		if colors[id] == 2 {
			return true
		}
		colors[id] = 1
		for _, p := range d.Assets[id].Value.ParentAssetIDs {
			if !visit(p) {
				return false
			}
		}
		colors[id] = 2
		return true
	}
	for id := range d.Assets {
		if !visit(id) {
			return errors.New("asset parent cycle")
		}
	}
	for key, r := range d.Idempotency {
		j, ok := d.Jobs[r.Value.JobID]
		scope := Scope{OwnerID: r.Scope.OwnerID, BackendID: r.Scope.BackendID}
		if !ok || j.Scope != r.Scope || r.Key == "" || r.Value.Hash == "" || key != idemIndex(scope, r.Key) {
			return errors.New("invalid idempotency record")
		}
	}
	for id, r := range d.Executions {
		j, ok := d.Jobs[id]
		if !ok || id != r.JobID || j.Scope != r.Scope {
			return errors.New("invalid execution record")
		}
		for item := range r.Items {
			found := false
			for _, i := range j.Value.Items {
				if i.ID == item {
					found = true
				}
			}
			if !found {
				return errors.New("invalid execution item")
			}
		}
	}
	return nil
}

// Keep os imported here to make the WAL file contract explicit at compile time.
var _ io.Writer = (*os.File)(nil)
