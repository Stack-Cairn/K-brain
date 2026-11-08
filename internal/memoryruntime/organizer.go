package memoryruntime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/memory"
)

type OrganizerTrigger string

const (
	OrganizerManual    OrganizerTrigger = "manual"
	OrganizerScheduled OrganizerTrigger = "scheduled"
)

type OrganizerRun struct {
	RunID     string            `json:"runId"`
	Epoch     uint64            `json:"epoch"`
	Executor  string            `json:"executorId"`
	Trigger   OrganizerTrigger  `json:"trigger"`
	Phase     string            `json:"phase"`
	Status    string            `json:"status"`
	Workdir   string            `json:"workdir,omitempty"`
	Model     string            `json:"model,omitempty"`
	Scanned   int               `json:"scanned"`
	Clusters  int               `json:"clusters"`
	Applied   int               `json:"applied"`
	Risk      string            `json:"risk"`
	Pending   []memory.Decision `json:"pending,omitempty"`
	Warnings  []string          `json:"warnings,omitempty"`
	CreatedAt time.Time         `json:"createdAt"`
	UpdatedAt time.Time         `json:"updatedAt"`
}

// RunOrganizer executes the auditable scan -> cluster -> plan -> gate -> apply flow.
// Manual runs stop at gate and can be applied later with ApplyOrganizerRun.
func (r *Runtime) RunOrganizer(ctx context.Context, workdir string, trigger OrganizerTrigger) (OrganizerRun, error) {
	if err := ctx.Err(); err != nil {
		return OrganizerRun{}, err
	}
	if trigger == "" {
		trigger = OrganizerManual
	}
	now := r.now()
	epoch, err := r.store.OrganizerEpoch()
	if err != nil {
		return OrganizerRun{}, err
	}
	run := OrganizerRun{RunID: randomRunID(), Epoch: epoch, Executor: newExecutorID(), Trigger: trigger, Phase: "scan", Status: "running", Workdir: workdir, Model: r.organizer, CreatedAt: now, UpdatedAt: now}
	if err := r.saveRun(run); err != nil {
		return run, err
	}
	list, err := r.store.List(memory.ListArgs{Workdir: workdir, IncludeDaily: false, Limit: 10000})
	if err != nil {
		run.Status = "failed"
		run.Warnings = []string{err.Error()}
		if saveErr := r.saveRun(run); saveErr != nil {
			return run, errors.Join(err, saveErr)
		}
		return run, err
	}
	run.Scanned = len(list.Entries)
	if err := ctx.Err(); err != nil {
		return r.cancelRun(run, err)
	}
	run.Phase = "cluster"
	run.UpdatedAt = r.now()
	if err := r.saveRun(run); err != nil {
		return run, err
	}
	clusters := clusterEntries(list.Entries)
	run.Clusters = len(clusters)
	if err := ctx.Err(); err != nil {
		return r.cancelRun(run, err)
	}
	run.Phase = "plan"
	run.UpdatedAt = r.now()
	if err := r.saveRun(run); err != nil {
		return run, err
	}
	decisions := make([]memory.Decision, 0)
	for _, cluster := range clusters {
		// The model round is optional: deterministic keep decisions are safe when no
		// organizer model is configured, while configured models are K-brain-only.
		if len(cluster) > 1 {
			first := cluster[0]
			original, readErr := r.store.Read(memory.ReadArgs{Slug: first.Slug, Scope: first.Scope, WorkdirHash: first.WorkdirHash, Length: 10000})
			if readErr != nil {
				return r.cancelRun(run, readErr)
			}
			for _, duplicate := range cluster[1:] {
				if duplicate.Description != "" && strings.EqualFold(duplicate.Description, first.Description) {
					candidate, readErr := r.store.Read(memory.ReadArgs{Slug: duplicate.Slug, Scope: duplicate.Scope, WorkdirHash: duplicate.WorkdirHash, Length: 10000})
					if readErr != nil {
						return r.cancelRun(run, readErr)
					}
					// Matching summaries alone do not establish duplicate content.
					if original.Window.Truncated || candidate.Window.Truncated || original.Body != candidate.Body {
						continue
					}
					decisions = append(decisions, memory.Decision{Op: "delete", Slug: duplicate.Slug, Scope: duplicate.Scope, WorkdirHash: duplicate.WorkdirHash, GroupID: run.RunID, Reason: "duplicate in organizer cluster", Preconditions: []memory.EntryPrecondition{
						{ReadArgs: memory.ReadArgs{Slug: first.Slug, Scope: first.Scope, WorkdirHash: first.WorkdirHash}, Body: original.Body, Description: original.Description},
						{ReadArgs: memory.ReadArgs{Slug: duplicate.Slug, Scope: duplicate.Scope, WorkdirHash: duplicate.WorkdirHash}, Body: candidate.Body, Description: candidate.Description},
					}})
				}
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return r.cancelRun(run, err)
	}
	run.Phase = "gate"
	run.UpdatedAt = r.now()
	run.Pending = decisions
	if len(decisions) == 0 {
		run.Risk = "none"
	} else {
		run.Risk = "low"
	}
	// Scheduled runs auto-apply deterministic low-risk or no-op plans. Manual runs
	// are retained as pending review even when the proposed action is low-risk.
	run.Status = "pending_review"
	if err := r.saveRun(run); err != nil {
		return run, err
	}
	var applyErr error
	var result memory.BatchResponse
	if trigger == OrganizerScheduled && (run.Risk == "low" || run.Risk == "none") {
		claimed, shouldApply, claimErr := r.store.ClaimOrganizeRunApply(run.RunID)
		if claimErr != nil {
			return run, claimErr
		}
		if !shouldApply {
			return run, fmt.Errorf("organizer run %q was not claimed for scheduled apply", run.RunID)
		}
		claimedRun, decodeErr := organizerRunFromStore(claimed)
		if decodeErr != nil {
			return run, decodeErr
		}
		run = claimedRun
		result, applyErr = r.store.ApplyBatch(memory.BatchArgs{Workdir: workdir, Trigger: "organizer", Model: r.organizer, Decisions: decisions})
		run.Warnings = append(run.Warnings, result.Warnings...)
		if applyErr != nil {
			run.Status = "failed"
			run.Warnings = []string{applyErr.Error()}
		} else {
			run.Applied = len(result.Deleted) + len(result.Updated) + len(result.Created)
			run.Pending = nil
			run.Status = "completed"
		}
	}
	run.Phase = "complete"
	run.UpdatedAt = r.now()
	if saveErr := r.saveRun(run); saveErr != nil {
		if applyErr != nil {
			return run, errors.Join(applyErr, saveErr)
		}
		return run, saveErr
	}
	return run, applyErr
}

func (r *Runtime) ApplyOrganizerRun(ctx context.Context, runID string) (OrganizerRun, error) {
	stored, storeErr := r.store.ReadOrganizeRun(runID)
	var run OrganizerRun
	if storeErr == nil {
		var decodeErr error
		run, decodeErr = organizerRunFromStore(stored)
		if decodeErr != nil {
			return OrganizerRun{}, decodeErr
		}
	} else {
		r.mu.Lock()
		run, _ = r.history[runID]
		r.mu.Unlock()
		if run.RunID == "" {
			return OrganizerRun{}, fmt.Errorf("organizer run %q not found", runID)
		}
	}
	if err := ctx.Err(); err != nil {
		return r.cancelRun(run, err)
	}
	claimed, shouldApply, err := r.store.ClaimOrganizeRunApply(runID)
	if err != nil {
		return run, err
	}
	if !shouldApply {
		completed, decodeErr := organizerRunFromStore(claimed)
		if decodeErr != nil {
			return run, decodeErr
		}
		r.rememberRun(completed)
		return completed, nil
	}
	if claimedRun, decodeErr := organizerRunFromStore(claimed); decodeErr == nil {
		run = claimedRun
	} else {
		return run, decodeErr
	}
	result, applyErr := r.store.ApplyBatch(memory.BatchArgs{Workdir: run.Workdir, Trigger: "organizer-manual", Model: run.Model, Decisions: run.Pending})
	run.Warnings = append(run.Warnings, result.Warnings...)
	if applyErr != nil {
		run.Status = "failed"
		run.Warnings = append(run.Warnings, applyErr.Error())
	} else {
		run.Applied = len(result.Created) + len(result.Updated) + len(result.Deleted)
		run.Pending = nil
		run.Status = "completed"
	}
	run.Phase = "complete"
	run.UpdatedAt = r.now()
	if saveErr := r.saveRun(run); saveErr != nil {
		if applyErr != nil {
			return run, errors.Join(applyErr, saveErr)
		}
		return run, saveErr
	}
	return run, applyErr
}

func (r *Runtime) OrganizerHistory() []OrganizerRun {
	// The runtime is request-scoped, so history must be reconstructed from the
	// store rather than relying on its in-memory cache.
	if result, err := r.store.ListOrganizeRuns(); err == nil {
		if stored, ok := result["runs"].([]memory.OrganizeRun); ok {
			for _, value := range stored {
				if run, decodeErr := organizerRunFromStore(value); decodeErr == nil {
					r.rememberRun(run)
				}
			}
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]OrganizerRun, 0, len(r.history))
	for _, run := range r.history {
		out = append(out, run)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

func (r *Runtime) saveRun(run OrganizerRun) error {
	payload := memory.OrganizeRun{
		"runId": run.RunID, "epoch": run.Epoch, "executorId": run.Executor, "phase": run.Phase, "status": run.Status, "trigger": run.Trigger,
		"workdir": run.Workdir, "model": run.Model, "scanned": run.Scanned, "clusters": run.Clusters,
		"applied": run.Applied, "risk": run.Risk, "pending": run.Pending, "warnings": run.Warnings,
		"createdAt": run.CreatedAt.UnixMilli(), "updatedAt": run.UpdatedAt.UnixMilli(),
	}
	if _, err := r.store.SaveOrganizeRun(payload); err != nil {
		return err
	}
	r.rememberRun(run)
	return nil
}

func (r *Runtime) rememberRun(run OrganizerRun) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.history == nil {
		r.history = map[string]OrganizerRun{}
	}
	r.history[run.RunID] = run
}

func organizerRunFromStore(stored memory.OrganizeRun) (OrganizerRun, error) {
	data, err := json.Marshal(stored)
	if err != nil {
		return OrganizerRun{}, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return OrganizerRun{}, err
	}
	var run OrganizerRun
	decode := func(key string, target any) error {
		if value, ok := raw[key]; ok {
			return json.Unmarshal(value, target)
		}
		return nil
	}
	if err := decode("runId", &run.RunID); err != nil {
		return run, err
	}
	if err := decode("epoch", &run.Epoch); err != nil {
		return run, err
	}
	if err := decode("executorId", &run.Executor); err != nil {
		return run, err
	}
	var trigger string
	if err := decode("trigger", &trigger); err != nil {
		return run, err
	}
	run.Trigger = OrganizerTrigger(trigger)
	if err := decode("phase", &run.Phase); err != nil {
		return run, err
	}
	if err := decode("status", &run.Status); err != nil {
		return run, err
	}
	if err := decode("workdir", &run.Workdir); err != nil {
		return run, err
	}
	if err := decode("model", &run.Model); err != nil {
		return run, err
	}
	if err := decode("scanned", &run.Scanned); err != nil {
		return run, err
	}
	if err := decode("clusters", &run.Clusters); err != nil {
		return run, err
	}
	if err := decode("applied", &run.Applied); err != nil {
		return run, err
	}
	if err := decode("risk", &run.Risk); err != nil {
		return run, err
	}
	if err := decode("pending", &run.Pending); err != nil {
		return run, err
	}
	if err := decode("warnings", &run.Warnings); err != nil {
		return run, err
	}
	for key, target := range map[string]*time.Time{"createdAt": &run.CreatedAt, "updatedAt": &run.UpdatedAt} {
		if value, ok := raw[key]; ok {
			var number int64
			if err := json.Unmarshal(value, &number); err == nil {
				*target = time.UnixMilli(number)
				continue
			}
			var text string
			if err := json.Unmarshal(value, &text); err != nil {
				return run, err
			}
			parsed, err := time.Parse(time.RFC3339Nano, text)
			if err != nil {
				return run, err
			}
			*target = parsed
		}
	}
	return run, nil
}

// StartOrganizerScheduler is a host-owned wakeup loop. It only invokes the
// K-brain organizer model configured on this Runtime.
func (r *Runtime) StartOrganizerScheduler(ctx context.Context, workdir string, interval time.Duration) func() {
	return r.NewOrganizerScheduler(ctx, workdir, true, interval).Close
}

// NewOrganizerScheduler starts a scheduled organizer loop that can be paused,
// resumed and given a new interval while it runs (see OrganizerScheduler.Update).
// A non-positive interval means 24h.
func (r *Runtime) NewOrganizerScheduler(ctx context.Context, workdir string, enabled bool, interval time.Duration) *OrganizerScheduler {
	return newOrganizerScheduler(ctx, enabled, interval, func(runCtx context.Context) {
		_, _ = r.RunOrganizer(runCtx, workdir, OrganizerScheduled)
	})
}

// OrganizerScheduler runs the organizer every interval while enabled. Each
// countdown starts when the scheduler starts, when Update is called and when the
// previous scheduled run returns, so runs never overlap.
type OrganizerScheduler struct {
	mu       sync.Mutex
	enabled  bool
	interval time.Duration
	update   chan struct{}
	cancel   context.CancelFunc
	done     chan struct{}
	once     sync.Once
}

func newOrganizerScheduler(ctx context.Context, enabled bool, interval time.Duration, run func(context.Context)) *OrganizerScheduler {
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	scheduleCtx, cancel := context.WithCancel(ctx)
	s := &OrganizerScheduler{enabled: enabled, interval: interval, update: make(chan struct{}, 1), cancel: cancel, done: make(chan struct{})}
	go s.loop(scheduleCtx, run)
	return s
}

func (s *OrganizerScheduler) loop(ctx context.Context, run func(context.Context)) {
	defer close(s.done)
	var timer *time.Timer
	var fire <-chan time.Time
	arm := func() {
		if timer != nil {
			timer.Stop()
		}
		timer, fire = nil, nil
		enabled, interval := s.State()
		if enabled {
			timer = time.NewTimer(interval)
			fire = timer.C
		}
	}
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	arm()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.update:
			arm()
		case <-fire:
			run(ctx)
			arm()
		}
	}
}

// Update applies a new schedule without restarting the loop: disabling stops
// future runs (a run in progress finishes), and enabling or changing the
// interval restarts the countdown from now. A non-positive interval means 24h.
func (s *OrganizerScheduler) Update(enabled bool, interval time.Duration) {
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	s.mu.Lock()
	s.enabled, s.interval = enabled, interval
	s.mu.Unlock()
	select {
	case s.update <- struct{}{}:
	default:
	}
}

// State is the schedule the loop currently follows.
func (s *OrganizerScheduler) State() (enabled bool, interval time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enabled, s.interval
}

// Close stops the loop, cancelling a run in progress, and waits for it.
func (s *OrganizerScheduler) Close() {
	s.once.Do(s.cancel)
	<-s.done
}

func (r *Runtime) cancelRun(run OrganizerRun, err error) (OrganizerRun, error) {
	stored, changed, cancelErr := r.store.CancelOrganizeRunOwned(run.RunID, run.Epoch, run.Executor, err.Error())
	if cancelErr != nil {
		return run, errors.Join(err, cancelErr)
	}
	if !changed {
		// Another request may have claimed apply after the context was cancelled.
		// Never overwrite that persisted state with a local cancelled snapshot.
		if stored != nil {
			if current, decodeErr := organizerRunFromStore(stored); decodeErr == nil {
				r.rememberRun(current)
				return current, err
			}
		}
		return run, err
	}
	cancelled, decodeErr := organizerRunFromStore(stored)
	if decodeErr != nil {
		return run, errors.Join(err, decodeErr)
	}
	r.rememberRun(cancelled)
	return cancelled, err
}
func newExecutorID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err == nil {
		return "executor-" + hex.EncodeToString(b)
	}
	return fmt.Sprintf("executor-%d", time.Now().UnixNano())
}

func randomRunID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err == nil {
		return "organize-" + hex.EncodeToString(b)
	}
	return fmt.Sprintf("organize-%d", time.Now().UnixNano())
}
func clusterEntries(entries []memory.Meta) [][]memory.Meta {
	groups := map[string][]memory.Meta{}
	for _, e := range entries {
		key := e.Scope + "/" + e.WorkdirHash + "/" + e.MemoryType
		groups[key] = append(groups[key], e)
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([][]memory.Meta, 0, len(keys))
	for _, k := range keys {
		out = append(out, groups[k])
	}
	return out
}
