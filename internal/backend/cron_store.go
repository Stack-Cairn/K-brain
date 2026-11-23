package backend

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/session"
)

const (
	cronRunLimit  = 200
	cronRunAge    = 30 * 24 * time.Hour
	maxCronOutput = 50000
)

type cronStore struct {
	mu   sync.Mutex
	path string
	disk cronDisk
}

func newCronStore(s *session.Store) (*cronStore, error) {
	c := &cronStore{path: filepath.Join(s.SessionsDir(), "automation-cron.json"), disk: cronDisk{Tasks: []*CronTask{}, Runs: []CronRunRecord{}}}
	b, e := os.ReadFile(c.path)
	if errors.Is(e, os.ErrNotExist) {
		return c, nil
	}
	if e != nil {
		return nil, e
	}
	if e = json.Unmarshal(b, &c.disk); e != nil {
		return nil, e
	}
	if c.disk.Tasks == nil {
		c.disk.Tasks = []*CronTask{}
	}
	if c.disk.Runs == nil {
		c.disk.Runs = []CronRunRecord{}
	}
	if e = validateCronTasks(c.disk.Tasks); e != nil {
		return nil, e
	}
	changed := false
	for i := range c.disk.Runs {
		r := &c.disk.Runs[i]
		if r.State == "leased" || r.State == "pending" {
			n := time.Now().UnixMilli()
			r.State = "expired"
			r.FinishedAt = &n
			r.Output = "Scheduled run interrupted by backend restart."
			r.DurationMs = uint64(max(int64(0), n-r.StartedAt))
			if r.Counted {
				if task := c.task(r.TaskID); task != nil && task.RemainingExecutions != nil && *task.RemainingExecutions > 0 {
					*task.RemainingExecutions--
					if *task.RemainingExecutions == 0 {
						task.Enabled = false
					}
					c.disk.Revision++
				}
			}
			changed = true
		}
	}
	if changed {
		e = c.persistLocked()
	}
	return c, e
}
func (c *cronStore) persistLocked() error {
	if e := os.MkdirAll(filepath.Dir(c.path), 0700); e != nil {
		return e
	}
	b, e := json.MarshalIndent(c.disk, "", "  ")
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(c.path), ".automation-cron-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.Write(b); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return os.Rename(f.Name(), c.path)
}
func cloneCronTasks(in []*CronTask) []*CronTask {
	o := make([]*CronTask, len(in))
	for i, t := range in {
		if t != nil {
			b, _ := json.Marshal(t)
			var x CronTask
			_ = json.Unmarshal(b, &x)
			o[i] = &x
		}
	}
	return o
}

const cronMaskedHeaderValue = "__liveagent-masked__"

func maskCronTaskHeaders(tasks []*CronTask) {
	for _, task := range tasks {
		if task == nil {
			continue
		}
		for i := range task.Requests {
			for key := range task.Requests[i].Headers {
				task.Requests[i].Headers[key] = cronMaskedHeaderValue
			}
		}
	}
}

func restoreCronMaskedHeaders(incoming map[string]any, stored []CronHTTPRequest) {
	raw, ok := incoming["requests"].([]any)
	if !ok {
		return
	}
	for i := range raw {
		request, ok := raw[i].(map[string]any)
		if !ok {
			continue
		}
		id, _ := request["id"].(string)
		var old *CronHTTPRequest
		for j := range stored {
			if stored[j].ID == id {
				old = &stored[j]
				break
			}
		}
		if old == nil {
			continue
		}
		headers, ok := request["headers"].(map[string]any)
		if !ok {
			continue
		}
		for key, value := range headers {
			if text, ok := value.(string); ok && text == cronMaskedHeaderValue {
				if storedValue, exists := old.Headers[key]; exists {
					headers[key] = storedValue
				} else {
					delete(headers, key)
				}
			}
		}
	}
}

func (c *cronStore) snapshotLocked() CronSnapshot {
	tasks := cloneCronTasks(c.disk.Tasks)
	maskCronTaskHeaders(tasks)
	return CronSnapshot{Revision: c.disk.Revision, Tasks: tasks}
}
func (c *cronStore) snapshot() CronSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snapshotLocked()
}
func (c *cronStore) task(id string) *CronTask {
	for _, t := range c.disk.Tasks {
		if t != nil && t.ID == id {
			return t
		}
	}
	return nil
}
func (c *cronStore) apply(in CronApplyInput) (CronApplyResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if in.BaseRevision != c.disk.Revision {
		return CronApplyResponse{"conflict", c.snapshotLocked()}, nil
	}
	next := cloneCronTasks(c.disk.Tasks)
	for _, op := range in.Ops {
		var e error
		next, e = applyCronOperation(next, op)
		if e != nil {
			return CronApplyResponse{}, e
		}
	}
	if e := validateCronTasks(next); e != nil {
		return CronApplyResponse{}, e
	}
	old := c.disk
	c.disk.Tasks = next
	present := make(map[string]bool, len(next))
	for _, task := range next {
		if task != nil {
			present[task.ID] = true
		}
	}
	keptRuns := make([]CronRunRecord, 0, len(c.disk.Runs))
	for _, run := range c.disk.Runs {
		if present[run.TaskID] {
			keptRuns = append(keptRuns, run)
		}
	}
	c.disk.Runs = keptRuns
	c.disk.Revision++
	if e := c.persistLocked(); e != nil {
		c.disk = old
		return CronApplyResponse{}, e
	}
	return CronApplyResponse{"ok", c.snapshotLocked()}, nil
}
func applyCronOperation(ts []*CronTask, op CronOperation) ([]*CronTask, error) {
	switch op.Op {
	case "create":
		b, e := json.Marshal(op.Item)
		if e != nil {
			return nil, e
		}
		t := &CronTask{TimeoutSeconds: 300, Type: "bash"}
		if e = json.Unmarshal(b, t); e != nil {
			return nil, e
		}

		if t.SessionID != "" {
			return nil, errors.New("cron sessionId is backend-owned")
		}
		if t.Loop != nil {
			return nil, errors.New("loop schedule is backend-owned")
		}
		if strings.HasPrefix(t.ID, loopIDPrefix) {
			return nil, errors.New("cron task ids starting with " + loopIDPrefix + " are reserved for conversation loops")
		}
		if t.ID == "" {
			t.ID = newRunID()
		}
		for _, x := range ts {
			if x.ID == t.ID {
				return nil, errors.New("duplicate cron task id")
			}
		}
		return append(ts, t), nil
	case "update":
		if _, supplied := op.Patch["loop"]; supplied {
			return nil, errors.New("loop schedule is backend-owned")
		}
		if _, supplied := op.Patch["sessionId"]; supplied {
			return nil, errors.New("cron sessionId is backend-owned")
		}
		for i, x := range ts {
			if x.ID == op.ID {
				if x.Loop != nil {
					for _, field := range []string{"cron", "type", "workdir"} {
						if _, supplied := op.Patch[field]; supplied {
							return nil, errors.New("change a loop's schedule by cancelling and recreating it")
						}
					}
				}
				b, _ := json.Marshal(x)
				m := map[string]any{}
				_ = json.Unmarshal(b, &m)
				restoreCronMaskedHeaders(op.Patch, x.Requests)
				for k, v := range op.Patch {
					if k != "id" {
						m[k] = v
					}
				}
				b, _ = json.Marshal(m)
				y := &CronTask{TimeoutSeconds: 300}
				if e := json.Unmarshal(b, y); e != nil {
					return nil, e
				}
				y.ID = x.ID
				if y.Workdir != x.Workdir || y.Type != x.Type {
					y.SessionID = ""
				}
				y.LastError = ""
				ts[i] = y
				return ts, nil
			}
		}
		return nil, errors.New("cron task not found")
	case "delete":
		for i, x := range ts {
			if x.ID == op.ID {
				return append(ts[:i], ts[i+1:]...), nil
			}
		}
		return nil, errors.New("cron task not found")
	case "reorder":
		if len(op.IDs) != len(ts) {
			return nil, errors.New("reorder must include every cron task")
		}
		m := map[string]*CronTask{}
		for _, x := range ts {
			m[x.ID] = x
		}
		out := make([]*CronTask, 0, len(ts))
		for _, id := range op.IDs {
			x := m[id]
			if x == nil {
				return nil, errors.New("reorder contains unknown cron task")
			}
			out = append(out, x)
			delete(m, id)
		}
		if len(m) > 0 {
			return nil, errors.New("reorder contains duplicate cron task")
		}
		return out, nil
	}
	return nil, errors.New("unsupported cron operation")
}
func (c *cronStore) bindSession(expected *CronTask, sessionID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	task := c.task(expected.ID)
	if task == nil {
		return errors.New("cron task not found")
	}
	if task.Workdir != expected.Workdir || task.Type != expected.Type || task.SessionID != expected.SessionID {
		return errors.New("cron task changed before session binding")
	}
	if task.Type != "prompt" {
		return errors.New("only prompt cron tasks have sessions")
	}
	if strings.TrimSpace(sessionID) == "" {
		return errors.New("cron session id is required")
	}
	if task.SessionID == sessionID {
		return nil
	}
	old := cloneCronDisk(c.disk)
	task.SessionID = strings.TrimSpace(sessionID)
	c.disk.Revision++
	return c.commitLocked(old)
}

func (c *cronStore) reserve(id string, counted bool) (*CronTask, CronRunRecord, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.task(id)
	if t == nil {
		return nil, CronRunRecord{}, errors.New("cron task not found")
	}
	if counted && (!t.Enabled || (t.RemainingExecutions != nil && *t.RemainingExecutions == 0)) {
		return nil, CronRunRecord{}, errors.New("cron task is disabled")
	}
	for _, r := range c.disk.Runs {
		if r.TaskID == id && r.State == "leased" {
			return nil, CronRunRecord{}, errors.New("Cron task is already running.")
		}
	}
	old := cloneCronDisk(c.disk)
	if t.Loop != nil {
		now := time.Now().UnixMilli()
		if now >= t.Loop.ExpiresAt {
			return nil, CronRunRecord{}, errors.New("loop has expired")
		}
		if counted && now < t.Loop.NextFireAt {
			return nil, CronRunRecord{}, errors.New("loop is not due")
		}
		if counted {
			t.Loop.NextFireAt = now + t.Loop.IntervalSeconds*1000
		}
		t.Loop.Iterations++
		c.disk.Revision++
	}
	r := CronRunRecord{ID: newRunID(), TaskID: id, State: "leased", StartedAt: time.Now().UnixMilli(), Counted: counted}
	c.disk.Runs = append(c.disk.Runs, r)
	if e := c.persistLocked(); e != nil {
		c.disk = old
		return nil, CronRunRecord{}, e
	}
	return cloneCronTasks([]*CronTask{t})[0], r, nil
}
func (c *cronStore) recordSkipped(taskID string) error {
	now := time.Now().UnixMilli()
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.recordLocked(CronRunRecord{
		ID:         newRunID(),
		TaskID:     taskID,
		State:      "done",
		Success:    false,
		StartedAt:  now,
		FinishedAt: &now,
		Output:     "Skipped: previous run is still in progress.",
	}, false, true)
}

func (c *cronStore) record(r CronRunRecord, counted bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.recordLocked(r, counted, true)
}

func (c *cronStore) recordCompletion(r CronRunRecord, counted bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.recordLocked(r, counted, false)
}

func (c *cronStore) recordLocked(r CronRunRecord, counted, allowNew bool) error {
	if c.task(r.TaskID) == nil {
		return nil
	}
	old := cloneCronDisk(c.disk)
	if x := []rune(r.Output); len(x) > maxCronOutput {
		r.Output = string(x[:maxCronOutput])
	}
	found := false
	for i := range c.disk.Runs {
		if c.disk.Runs[i].ID == r.ID {
			previous := c.disk.Runs[i]
			if previous.TaskID != r.TaskID || previous.State == "done" || previous.State == "expired" {
				return nil
			}
			c.disk.Runs[i] = r
			found = true
		}
	}
	// Missing leases were deleted or cleared; late workers must not recreate them.
	if !found {
		if !allowNew {
			return nil
		}
		c.disk.Runs = append(c.disk.Runs, r)
	}
	if counted && (r.State == "done" || r.State == "expired") {
		if t := c.task(r.TaskID); t != nil && t.RemainingExecutions != nil {
			if *t.RemainingExecutions > 0 {
				*t.RemainingExecutions--
			}
			if *t.RemainingExecutions == 0 {
				t.Enabled = false
			}
			c.disk.Revision++
		}
	}
	cut := time.Now().Add(-cronRunAge).UnixMilli()
	keep := make([]CronRunRecord, 0, len(c.disk.Runs))
	per := map[string]int{}
	for i := len(c.disk.Runs) - 1; i >= 0; i-- {
		x := c.disk.Runs[i]
		if x.FinishedAt != nil && x.StartedAt < cut {
			continue
		}
		if x.FinishedAt != nil {
			if per[x.TaskID] >= cronRunLimit {
				continue
			}
			per[x.TaskID]++
		}
		keep = append(keep, x)
	}
	// keep was collected newest first; restore insertion order before sorting so
	// runs started in the same millisecond keep their order instead of flipping
	// on every prune and listing the older run as the latest.
	slices.Reverse(keep)
	sort.SliceStable(keep, func(i, j int) bool { return keep[i].StartedAt < keep[j].StartedAt })
	c.disk.Runs = keep
	return c.commitLocked(old)
}
func (c *cronStore) runs(id string, limit int) ([]CronRunRecord, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.task(id) == nil {
		return nil, errors.New("cron task not found")
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	out := []CronRunRecord{}
	for i := len(c.disk.Runs) - 1; i >= 0 && len(out) < limit; i-- {
		if c.disk.Runs[i].TaskID == id {
			out = append(out, c.disk.Runs[i])
		}
	}
	return out, nil
}
func (c *cronStore) clearRuns(id string) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.task(id) == nil {
		return 0, errors.New("cron task not found")
	}
	old := cloneCronDisk(c.disk)
	n := 0
	out := []CronRunRecord{}
	for _, r := range c.disk.Runs {
		if r.TaskID == id && (r.State == "done" || r.State == "expired") {
			n++
		} else {
			out = append(out, r)
		}
	}
	c.disk.Runs = out
	if n > 0 {
		if e := c.commitLocked(old); e != nil {
			return 0, e
		}
	}
	return n, nil
}
func (c *cronStore) setError(id, msg string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.task(id)
	if t == nil || t.LastError == msg {
		return nil
	}
	old := cloneCronDisk(c.disk)
	t.LastError = msg
	c.disk.Revision++
	return c.commitLocked(old)
}

func (c *cronStore) disableError(id, msg string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.task(id)
	if t == nil || (!t.Enabled && t.LastError == msg) {
		return nil
	}
	old := cloneCronDisk(c.disk)
	t.Enabled = false
	t.LastError = msg
	c.disk.Revision++
	return c.commitLocked(old)
}

func cloneCronDisk(d cronDisk) cronDisk {
	d.Tasks = cloneCronTasks(d.Tasks)
	d.Runs = append([]CronRunRecord(nil), d.Runs...)
	return d
}

func (c *cronStore) commitLocked(old cronDisk) error {
	if err := c.persistLocked(); err != nil {
		c.disk = old
		return err
	}
	return nil
}
