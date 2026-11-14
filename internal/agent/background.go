package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/sandbox"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

type TaskStatus string

const (
	TaskRunning   TaskStatus = "running"
	TaskDone      TaskStatus = "done"
	TaskError     TaskStatus = "error"
	TaskCancelled TaskStatus = "cancelled"
)

type BackgroundTask struct {
	ID          string
	Description string
	Prompt      string
	Status      TaskStatus
	Report      string
	StartedAt   time.Time
	EndedAt     time.Time
	// ParentToolCallID is the parent's subagent tool call that started it.
	ParentToolCallID string

	Restored    bool
	FollowingUp bool
	finalizing  bool

	Done chan struct{}

	ctx       context.Context
	cancel    context.CancelFunc
	cancelled func() bool

	sub          *Agent
	worktreePath string
	followCancel context.CancelFunc

	SubMessages []ai.Message

	SubUsage      ai.Usage
	SubModelUsage map[string]ai.Usage
	SubSubUsage   map[string]ai.Usage

	SubModel string
}

type taskRegistry struct {
	mu    sync.Mutex
	tasks map[string]*BackgroundTask

	journals map[string]*taskJournal

	subs map[string][]*taskSubscription

	OnChange func(*BackgroundTask)

	OnRecord  func(sessionID string, t *BackgroundTask)
	OnEvents  func(taskID string) Events
	sessionID atomic.Pointer[string]

	// workers counts the goroutines that run background tasks, up to and including
	// the report notice each delivers; idle is closed whenever it drops to zero.
	workers int
	idle    chan struct{}
}

type taskSubscription struct {
	events Events
}

func (r *taskRegistry) SetSessionID(id string) {
	if id == "" {
		r.sessionID.Store(nil)
		return
	}
	r.sessionID.Store(&id)
}

func (r *taskRegistry) recordSession() string {
	if p := r.sessionID.Load(); p != nil {
		return *p
	}
	return ""
}

func newTaskRegistry() *taskRegistry {
	return &taskRegistry{tasks: map[string]*BackgroundTask{}, subs: map[string][]*taskSubscription{}, journals: map[string]*taskJournal{}}
}

func (r *taskRegistry) List() []BackgroundTask {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]BackgroundTask, 0, len(r.tasks))
	for _, t := range r.tasks {
		out = append(out, *t)
	}

	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].StartedAt.Before(out[j].StartedAt)
		}
		return taskIDNum(out[i].ID) < taskIDNum(out[j].ID)
	})
	return out
}

func taskIDNum(id string) int64 {
	if i := strings.LastIndexByte(id, '-'); i >= 0 {
		n, _ := strconv.ParseInt(id[i+1:], 10, 64)
		return n
	}
	return 0
}

func taskSlug(description string, n int64) string {
	words := strings.FieldsFunc(strings.ToLower(description), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
	var kept []string
	for _, w := range words {
		if w != "" {
			kept = append(kept, w)
		}
		if len(kept) == 5 {
			break
		}
	}
	slug := strings.Join(kept, "-")
	if slug == "" {
		slug = "sub"
	}
	return fmt.Sprintf("%s-%d", slug, n)
}

func (r *taskRegistry) Get(id string) (BackgroundTask, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.tasks[id]
	if !ok {
		return BackgroundTask{}, false
	}
	return *t, true
}

func (r *taskRegistry) ClearSettled(keep ...string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for id, t := range r.tasks {
		if !slices.Contains(keep, id) && t.Status != TaskRunning && !t.FollowingUp {
			if t.finalizing {
				continue
			}
			delete(r.tasks, id)
			delete(r.subs, id)
			delete(r.journals, id)
			n++
		}
	}
	return n
}

func (r *taskRegistry) Cancel(id string) bool {
	r.mu.Lock()
	t, ok := r.tasks[id]
	var cancel context.CancelFunc
	if ok {
		if t.FollowingUp {
			cancel = t.followCancel
		} else if t.Status == TaskRunning {
			cancel = t.cancel
		}
	}
	r.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

// CancelRunning requests cancellation of every running task and follow-up, and
// reports how many it reached. Each task still settles on its own goroutine.
func (r *taskRegistry) CancelRunning() int {
	r.mu.Lock()
	var cancels []context.CancelFunc
	for _, t := range r.tasks {
		switch {
		case t.FollowingUp && t.followCancel != nil:
			cancels = append(cancels, t.followCancel)
		case t.Status == TaskRunning && t.cancel != nil:
			cancels = append(cancels, t.cancel)
		}
	}
	r.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	return len(cancels)
}

// goWorker runs fn on a goroutine that Wait waits for.
func (r *taskRegistry) goWorker(fn func()) {
	r.mu.Lock()
	if r.workers == 0 {
		r.idle = make(chan struct{})
	}
	r.workers++
	r.mu.Unlock()
	go func() {
		defer func() {
			r.mu.Lock()
			r.workers--
			if r.workers == 0 {
				close(r.idle)
			}
			r.mu.Unlock()
		}()
		fn()
	}()
}

// Wait blocks until every background task goroutine has returned: the task has
// settled, OnRecord has run and its report notice has been delivered. Tasks keep
// running until they end on their own, so callers that shut down cancel them
// first (CancelRunning). Tasks started while Wait blocks are waited for too.
func (r *taskRegistry) Wait() {
	for {
		r.mu.Lock()
		if r.workers == 0 {
			r.mu.Unlock()
			return
		}
		idle := r.idle
		r.mu.Unlock()
		<-idle
	}
}

func (r *taskRegistry) settle(id string, status TaskStatus, report string) {
	r.mu.Lock()
	t, ok := r.tasks[id]
	if !ok || t.Status != TaskRunning {
		r.mu.Unlock()
		return
	}
	t.Status, t.Report, t.EndedAt = status, report, time.Now()
	t.finalizing = true
	snapshot := *t
	delete(r.subs, id)
	r.mu.Unlock()

	if r.OnChange != nil {
		r.OnChange(&snapshot)
	}
	if r.OnRecord != nil {
		r.OnRecord(r.recordSession(), &snapshot)
	}
	r.mu.Lock()
	t.finalizing = false
	close(t.Done)
	r.mu.Unlock()
}

var taskIDCounter atomic.Int64

func (a *Agent) StartBackground(description, prompt string, o SubModel) *BackgroundTask {
	t := a.RegisterBackground(description, prompt, o)
	if a.WorktreeSubagents {
		a.Tasks().goWorker(func() {
			if _, err := a.prepareBackground(t, true); err != nil {
				a.Notify(fmt.Sprintf("[subagent %s] %s", t.ID, err))
			}
		})
	} else {
		a.launchBackground(t)
	}
	return t
}

func (a *Agent) prepareBackground(t *BackgroundTask, worktree bool) (string, error) {
	path := ""
	var err error
	if worktree {
		path, err = provisionSubagentWorktreeAt(t.ctx, t.sub.WorkingDir, t.ID)
	}
	if err == nil {
		err = t.ctx.Err()
	}
	if err != nil {
		status := TaskError
		if t.ctx.Err() != nil {
			status = TaskCancelled
		}
		if path != "" {
			err = fmt.Errorf("prepare subagent workspace at %s: %w", path, err)
		} else {
			err = fmt.Errorf("prepare subagent workspace: %w", err)
		}
		t.cancel()
		a.Tasks().settle(t.ID, status, err.Error())
		return "", err
	}
	a.LaunchBackground(t, path)
	return path, nil
}

func (a *Agent) RegisterBackground(description, prompt string, o SubModel) *BackgroundTask {
	return a.RegisterBackgroundContext(context.Background(), description, prompt, o)
}

func (a *Agent) RegisterBackgroundContext(parent context.Context, description, prompt string, o SubModel) *BackgroundTask {
	r := a.Tasks()
	base := parent
	if base == nil {
		base = context.Background()
	}
	if a.SandboxPolicy != nil && sandbox.FromContext(base) == nil {
		base = sandbox.WithPolicy(base, a.SandboxPolicy)
	}
	// The task reports after the turn that started it has ended, which is the point
	// of a background subagent; stopping that turn still cancels it.
	outlive, release := tools.OutliveTurn(base)
	taskCtx, cancelTask := context.WithCancel(outlive)
	cancel := func() { cancelTask(); release() }
	sub := a.newSubContext(base, o)

	scope := a.SessionIDValue()
	if scope == "" {
		scope = a.cacheKey
	}
	r.mu.Lock()
	id := taskSlug(description, taskIDCounter.Add(1))
	for r.tasks[id] != nil {
		id = taskSlug(description, taskIDCounter.Add(1))
	}
	t := &BackgroundTask{
		ID: id, Description: description, Prompt: prompt,
		Status: TaskRunning, StartedAt: time.Now(), ParentToolCallID: tools.ToolCallID(base),
		Done: make(chan struct{}), ctx: withRequestTask(taskCtx, id), cancel: cancel,
		cancelled: func() bool {
			return taskCtx.Err() != nil || (base.Err() != nil && !errors.Is(context.Cause(base), tools.ErrTurnEnded))
		},
		sub: sub,

		SubModel: sub.Model,
	}
	r.tasks[id] = t
	r.mu.Unlock()
	if scope != "" {
		sub.SetCacheKey(scope + "/" + id)
	}
	if r.OnChange != nil {
		r.OnChange(t)
	}
	if r.OnRecord != nil {
		r.OnRecord(r.recordSession(), t)
	}
	return t
}

// WaitCancelledTasks waits for cancelled background tasks to finish publishing and
// recording their terminal state. It neither cancels nor waits for live tasks:
// their lifetime still belongs to the context passed at registration.
func (a *Agent) WaitCancelledTasks() {
	r := a.Tasks()
	r.mu.Lock()
	var done []<-chan struct{}
	for _, t := range r.tasks {
		// OutliveTurn propagates stop via AfterFunc. The parent can already be
		// cancelled while the child's callback has not run yet. Normal completion
		// deliberately leaves background work alive and must not join it.
		if t.cancelled != nil && t.cancelled() && t.Done != nil {
			done = append(done, t.Done)
		}
	}
	r.mu.Unlock()
	for _, ch := range done {
		<-ch
	}
}

func (a *Agent) LaunchBackground(t *BackgroundTask, worktreePath string) {
	if worktreePath != "" {
		a.bg.mu.Lock()
		t.worktreePath = worktreePath
		t.ctx = t.sub.setWorktree(t.ctx, worktreePath)
		a.bg.mu.Unlock()
		t.sub.Steer("Work entirely inside the git worktree at " + worktreePath + ". Your tools already use this working directory; its branch is isolated from other agents.")
	}
	a.launchBackground(t)
}

func (a *Agent) launchBackground(t *BackgroundTask) {
	id, description, prompt := t.ID, t.Description, t.Prompt
	taskCtx := t.ctx
	if t.sub.SandboxPolicy != nil {
		taskCtx = sandbox.WithPolicy(taskCtx, t.sub.SandboxPolicy)
	}
	a.Tasks().goWorker(func() {
		defer t.cancel()

		report, err := t.sub.Turn(taskCtx, prompt, a.bg.emitter(id))
		status := TaskDone
		text := report
		switch {
		case taskCtx.Err() != nil:
			status, text = TaskCancelled, taskCtx.Err().Error()
		case err != nil:
			status, text = TaskError, err.Error()
		}
		if t.worktreePath != "" {
			text += "\n\nWorktree: " + t.worktreePath
		}

		a.bg.mu.Lock()
		t.SubMessages = t.sub.MessagesSnapshot()
		t.SubUsage, t.SubSubUsage = t.sub.Usage(), t.sub.SubUsage()
		t.SubModelUsage = t.sub.ModelUsage()
		a.bg.mu.Unlock()
		a.bg.settle(id, status, text)

		a.Notify(fmt.Sprintf("[subagent %s %s] %s\n\n%s", id, status, description, capReport(text)))
	})
}

func (r *taskRegistry) refreshTranscript(id string, sub *Agent) {
	r.mu.Lock()
	t, ok := r.tasks[id]
	if ok {
		t.SubMessages = sub.MessagesSnapshot()
		t.SubUsage, t.SubSubUsage = sub.Usage(), sub.SubUsage()
		t.SubModelUsage = sub.ModelUsage()
	}
	r.mu.Unlock()
	if !ok || r.OnRecord == nil {
		return
	}
	r.OnRecord(r.recordSession(), t)
}

// Progress returns a running background subagent's transcript and usage so
// far; ok is false once it is unknown or has no live agent (restored).
func (r *taskRegistry) Progress(id string) (msgs []ai.Message, usage ai.Usage, ok bool) {
	r.mu.Lock()
	t, exists := r.tasks[id]
	var sub *Agent
	if exists {
		sub = t.sub
	}
	r.mu.Unlock()
	if sub == nil {
		return nil, ai.Usage{}, false
	}
	return sub.MessagesSnapshot(), sub.TotalUsage(), true
}

func (r *taskRegistry) SubscribeWithJournal(id string, ev Events) (events []JournaledEvent, truncated, ok bool) {
	events, truncated, ok, _ = r.WatchTask(id, ev)
	return
}

func (r *taskRegistry) WatchTask(id string, ev Events) (events []JournaledEvent, truncated, live bool, unsubscribe func()) {
	unsubscribe = func() {}
	r.mu.Lock()
	defer r.mu.Unlock()
	t, exists := r.tasks[id]
	if !exists {
		return
	}
	j := r.journals[id]
	if j != nil {
		events = append([]JournaledEvent(nil), j.events...)
		truncated = j.Truncated
	}
	if t.Status != TaskRunning && !t.FollowingUp {
		return
	}
	live = true
	if ev.OnText == nil && ev.OnThink == nil && ev.OnToolStart == nil && ev.OnToolCall == nil && ev.OnToolEnd == nil && ev.OnToolResult == nil && ev.OnSteer == nil && ev.OnCompact == nil {
		return
	}
	sub := &taskSubscription{events: ev}
	r.subs[id] = append(r.subs[id], sub)
	unsubscribe = sync.OnceFunc(func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.subs[id] = slices.DeleteFunc(r.subs[id], func(s *taskSubscription) bool { return s == sub })
		if len(r.subs[id]) == 0 {
			delete(r.subs, id)
		}
	})
	return
}

func (r *taskRegistry) emitLocked(id string, kind int, s, s2 string, journaled bool) []Events {
	r.mu.Lock()
	defer r.mu.Unlock()
	if task, ok := r.tasks[id]; !ok || (task.Status != TaskRunning && !task.FollowingUp) {
		return nil
	}
	if journaled {
		j := r.journals[id]
		if j == nil {
			j = &taskJournal{}
			r.journals[id] = j
		}
		j.append(kind, s, s2)
	}
	out := make([]Events, 0, len(r.subs[id]))
	for _, sub := range r.subs[id] {
		out = append(out, sub.events)
	}
	return out
}

func (r *taskRegistry) emitter(id string) Events {
	if r.OnEvents != nil {
		return FanIn(r.localEmitter(id), r.OnEvents(id))
	}
	return r.localEmitter(id)
}

func (r *taskRegistry) localEmitter(id string) Events {
	return Events{
		OnText: func(s string) {
			subs := r.emitLocked(id, 0, s, "", true)
			for _, e := range subs {
				if e.OnText != nil {
					e.OnText(s)
				}
			}
		},
		OnThink: func(s string) {
			for _, e := range r.emitLocked(id, 0, "", "", false) {
				if e.OnThink != nil {
					e.OnThink(s)
				}
			}
		},
		OnToolStart: func(tcID, n, a string) {
			subs := r.emitLocked(id, 1, n, a, true)
			for _, e := range subs {
				if e.OnToolStart != nil {
					e.OnToolStart(tcID, n, a)
				}
			}
		},
		OnToolCall: func(tcID, n, a string) {
			for _, e := range r.emitLocked(id, 0, "", "", false) {
				if e.OnToolCall != nil {
					e.OnToolCall(tcID, n, a)
				}
			}
		},
		OnToolEnd: func(tcID, n, res string) {
			subs := r.emitLocked(id, 2, n, res, true)
			for _, e := range subs {
				if e.OnToolEnd != nil {
					e.OnToolEnd(tcID, n, res)
				}
			}
		},
		OnToolResult: func(tcID, name string, result tools.Result) {
			for _, e := range r.emitLocked(id, 0, "", "", false) {
				if e.OnToolResult != nil {
					e.OnToolResult(tcID, name, result)
				}
			}
		},
		OnSteer: func(s string) {
			subs := r.emitLocked(id, 3, s, "", true)
			for _, e := range subs {
				if e.OnSteer != nil {
					e.OnSteer(s)
				}
			}
		},
		OnCompact: func(took, kept int) {

			for _, e := range r.emitLocked(id, 0, "", "", false) {
				if e.OnCompact != nil {
					e.OnCompact(took, kept)
				}
			}
		},
	}
}

func (a *Agent) Tasks() *taskRegistry {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.bg == nil {
		a.bg = newTaskRegistry()
	}
	return a.bg
}

func (a *Agent) RestoreTask(t BackgroundTask) {
	r := a.Tasks()
	t.Done = make(chan struct{})
	close(t.Done)
	t.cancel = func() {}
	r.mu.Lock()
	if _, exists := r.tasks[t.ID]; exists {
		r.mu.Unlock()
		return
	}
	r.tasks[t.ID] = &t
	r.mu.Unlock()
}
