package agent

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
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

	Restored bool

	Done chan struct{}

	ctx    context.Context
	cancel context.CancelFunc

	sub *Agent

	SubMessages []ai.Message

	SubUsage    ai.Usage
	SubSubUsage map[string]ai.Usage

	SubModel string
}

type JournaledEvent struct {
	Kind  int
	S, S2 string
}

type taskJournal struct {
	events    []JournaledEvent
	bytes     int
	Truncated bool
}

const journalBudget = 128 * 1024

func (j *taskJournal) append(kind int, s, s2 string) {
	if kind == 0 && len(j.events) > 0 && j.events[len(j.events)-1].Kind == 0 {
		j.events[len(j.events)-1].S += s
		j.bytes += len(s)
	} else {
		j.events = append(j.events, JournaledEvent{Kind: kind, S: s, S2: s2})
		j.bytes += len(s) + len(s2)
	}
	for j.bytes > journalBudget && len(j.events) > 1 {
		j.bytes -= len(j.events[0].S) + len(j.events[0].S2)
		j.events = j.events[1:]
		j.Truncated = true
	}

	if len(j.events) == 1 && len(j.events[0].S) > journalBudget {
		drop := len(j.events[0].S) - journalBudget
		j.events[0].S = j.events[0].S[drop:]
		j.bytes -= drop
		j.Truncated = true
	}
}

type taskRegistry struct {
	mu    sync.Mutex
	tasks map[string]*BackgroundTask

	journals map[string]*taskJournal

	subs map[string][]Events

	OnChange func(*BackgroundTask)

	OnRecord  func(sessionID string, t *BackgroundTask)
	sessionID atomic.Pointer[string]
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
	return &taskRegistry{tasks: map[string]*BackgroundTask{}, subs: map[string][]Events{}, journals: map[string]*taskJournal{}}
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
		return r < 'a' || r > 'z' && (r < '0' || r > '9')
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
		if !slices.Contains(keep, id) && t.Status != TaskRunning {
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
	running := ok && t.Status == TaskRunning
	r.mu.Unlock()
	if !running {
		return false
	}
	t.cancel()
	return true
}

func (r *taskRegistry) settle(id string, status TaskStatus, report string) {
	r.mu.Lock()
	t, ok := r.tasks[id]
	if !ok {
		r.mu.Unlock()
		return
	}
	t.Status, t.Report, t.EndedAt = status, report, time.Now()
	r.mu.Unlock()

	if r.OnChange != nil {
		r.OnChange(t)
	}
	if r.OnRecord != nil {
		r.OnRecord(r.recordSession(), t)
	}
	close(t.Done)
}

var taskIDCounter atomic.Int64

func (a *Agent) StartBackground(description, prompt string, o SubModel) *BackgroundTask {
	t := a.RegisterBackground(description, prompt, o)
	a.launchBackground(t)
	return t
}

func (a *Agent) RegisterBackground(description, prompt string, o SubModel) *BackgroundTask {
	if a.bg == nil {
		a.bg = newTaskRegistry()
	}
	id := taskSlug(description, taskIDCounter.Add(1))
	taskCtx, cancel := context.WithCancel(context.Background())
	sub := a.newSub(o)

	scope := a.SessionIDValue()
	if scope == "" {
		scope = a.cacheKey
	}
	if scope != "" {
		sub.SetCacheKey(scope + "/" + id)
	}
	t := &BackgroundTask{
		ID: id, Description: description, Prompt: prompt,
		Status: TaskRunning, StartedAt: time.Now(),
		Done: make(chan struct{}), ctx: taskCtx, cancel: cancel,
		sub: sub,

		SubModel: sub.Model,
	}
	a.bg.mu.Lock()
	a.bg.tasks[id] = t
	a.bg.mu.Unlock()
	if a.bg.OnChange != nil {
		a.bg.OnChange(t)
	}
	if a.bg.OnRecord != nil {
		a.bg.OnRecord(a.bg.recordSession(), t)
	}
	return t
}

func (a *Agent) LaunchBackground(t *BackgroundTask, worktreePath string) {
	if worktreePath != "" {
		t.sub.Steer("Work entirely inside the git worktree at " + worktreePath + " (run `cd " + worktreePath + "` first; it is your own branch, isolated from other agents). Commit your changes there.")
	}
	a.launchBackground(t)
}

func (a *Agent) launchBackground(t *BackgroundTask) {
	id, description, prompt := t.ID, t.Description, t.Prompt
	taskCtx := t.ctx
	go func() {

		report, err := t.sub.Turn(taskCtx, prompt, a.bg.emitter(id))
		status := TaskDone
		text := report
		switch {
		case err != nil && taskCtx.Err() == context.Canceled:
			status, text = TaskCancelled, "cancelled"
		case err != nil:
			status, text = TaskError, err.Error()
		}

		a.bg.mu.Lock()
		t.SubMessages = t.sub.MessagesSnapshot()
		t.SubUsage, t.SubSubUsage = t.sub.Usage(), t.sub.SubUsage()
		a.bg.mu.Unlock()
		a.bg.settle(id, status, text)

		a.bg.mu.Lock()
		delete(a.bg.subs, id)
		a.bg.mu.Unlock()

		a.Steer(fmt.Sprintf("[subagent %s %s] %s\n\n%s", id, status, description, text))
	}()
}

func (r *taskRegistry) refreshTranscript(id string, sub *Agent) {
	r.mu.Lock()
	t, ok := r.tasks[id]
	if ok {
		t.SubMessages = sub.MessagesSnapshot()
		t.SubUsage, t.SubSubUsage = sub.Usage(), sub.SubUsage()
	}
	r.mu.Unlock()
	if !ok || r.OnRecord == nil {
		return
	}
	r.OnRecord(r.recordSession(), t)
}

func (r *taskRegistry) SubscribeWithJournal(id string, ev Events) (events []JournaledEvent, truncated, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, exists := r.tasks[id]
	if !exists {
		return nil, false, false
	}
	j := r.journals[id]
	if j != nil {
		events = append([]JournaledEvent(nil), j.events...)
		truncated = j.Truncated
	}
	if t.Status != TaskRunning {
		return events, truncated, false
	}
	r.subs[id] = append(r.subs[id], ev)
	return events, truncated, true
}

func (r *taskRegistry) emitLocked(id string, kind int, s, s2 string, journaled bool) []Events {
	r.mu.Lock()
	defer r.mu.Unlock()
	if journaled {
		j := r.journals[id]
		if j == nil {
			j = &taskJournal{}
			r.journals[id] = j
		}
		j.append(kind, s, s2)
	}
	return append([]Events(nil), r.subs[id]...)
}

func (r *taskRegistry) emitter(id string) Events {
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
	r.tasks[t.ID] = &t
	r.mu.Unlock()
}
