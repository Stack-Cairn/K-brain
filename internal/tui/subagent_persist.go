package tui

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/session"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// subagentSaveInterval bounds how often a running subagent's progress is saved.
const subagentSaveInterval = time.Second

// subagentRecorder saves kn's subagents, background and foreground, into the
// session store while they run: status, tool count, usage and a transcript
// snapshot. Other processes sharing the store (the LiveAgent backend) list and
// follow them from there; the task lease tells them the subagent is still live.
// Progress saves are throttled; the final save is immediate.
type subagentRecorder struct {
	st       *session.Store
	interval time.Duration

	mu   sync.Mutex
	live map[string]*recordedSubagent
}

type recordedSubagent struct {
	sessionID string
	task      session.Task
	progress  func() ([]ai.Message, ai.Usage)
	timer     *time.Timer
	done      bool

	// save serializes writes, so a throttled progress save never lands after
	// the final one.
	save sync.Mutex
}

func newSubagentRecorder(st *session.Store) *subagentRecorder {
	return &subagentRecorder{st: st, interval: subagentSaveInterval, live: map[string]*recordedSubagent{}}
}

// start saves a subagent that just started and keeps it for progress saves.
func (r *subagentRecorder) start(sessionID string, task session.Task, progress func() ([]ai.Message, ai.Usage)) {
	if r == nil || r.st == nil || sessionID == "" || task.ID == "" {
		return
	}
	lease, err := r.st.TaskLease(sessionID)
	if err != nil {
		config.LogEvent("session.task", "lease failed: "+err.Error())
	}
	task.Lease = lease
	rec := &recordedSubagent{sessionID: sessionID, task: task, progress: progress}
	r.mu.Lock()
	if prev := r.live[task.ID]; prev != nil {
		r.mu.Unlock()
		r.touch(task.ID, false)
		return
	}
	r.live[task.ID] = rec
	r.mu.Unlock()
	r.flush(rec)
}

// touch notes progress of a running subagent (task ids are unique within the
// process) and schedules a save.
func (r *subagentRecorder) touch(id string, toolStarted bool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	rec := r.live[id]
	if rec == nil || rec.done {
		return
	}
	if toolStarted {
		rec.task.ToolCount++
	}
	if rec.timer == nil {
		rec.timer = time.AfterFunc(r.interval, func() {
			r.mu.Lock()
			rec.timer = nil
			r.mu.Unlock()
			r.flush(rec)
		})
	}
}

// finish saves a subagent's final state and transcript and forgets it.
func (r *subagentRecorder) finish(sessionID string, final session.Task, msgs []ai.Message, usage ai.UsageSummary) {
	if r == nil || r.st == nil || sessionID == "" {
		return
	}
	r.mu.Lock()
	rec := r.live[final.ID]
	delete(r.live, final.ID)
	if rec != nil {
		rec.done = true
		if rec.timer != nil {
			rec.timer.Stop()
			rec.timer = nil
		}
		final.ToolCount = max(final.ToolCount, rec.task.ToolCount)
		final.Lease = rec.task.Lease
		final.Foreground = final.Foreground || rec.task.Foreground
		if final.ParentToolCallID == "" {
			final.ParentToolCallID = rec.task.ParentToolCallID
		}
		if final.Model == "" {
			final.Model, final.Provider = rec.task.Model, rec.task.Provider
		}
	} else {
		rec = &recordedSubagent{sessionID: sessionID, done: true}
	}
	r.mu.Unlock()
	if usage.Total != (ai.Usage{}) {
		u := usage.Total
		final.Usage = &u
	}
	rec.save.Lock()
	defer rec.save.Unlock()
	r.write(sessionID, final, msgs, &usage)
}

// flush writes a running subagent's current progress unless it has finished.
func (r *subagentRecorder) flush(rec *recordedSubagent) {
	rec.save.Lock()
	defer rec.save.Unlock()
	var msgs []ai.Message
	var usage ai.Usage
	if rec.progress != nil {
		msgs, usage = rec.progress()
	}
	r.mu.Lock()
	if rec.done {
		r.mu.Unlock()
		return
	}
	task := rec.task
	r.mu.Unlock()
	if usage != (ai.Usage{}) {
		task.Usage = &usage
	}
	r.write(rec.sessionID, task, msgs, nil)
}

// write saves the record, then the transcript; a final save also stores the
// transcript's usage.
func (r *subagentRecorder) write(sessionID string, task session.Task, msgs []ai.Message, usage *ai.UsageSummary) {
	if err := r.st.SaveTask(sessionID, task); err != nil {
		config.LogEvent("session.task", "save failed: "+err.Error())
		return
	}
	if len(msgs) == 0 {
		return
	}
	id, err := r.st.SaveSubagentTranscript(sessionID, task.ID, msgs, task.Model, task.Provider)
	if err != nil {
		config.LogEvent("session.task", "transcript save failed: "+err.Error())
		return
	}
	if usage != nil && id != "" {
		_ = r.st.SetUsage(id, *usage)
	}
}

// events reports a running subagent's progress to the recorder.
func (r *subagentRecorder) events(id string) agent.Events {
	if r == nil {
		return agent.Events{}
	}
	return agent.Events{
		OnToolStart:  func(string, string, string) { r.touch(id, true) },
		OnToolResult: func(string, string, tools.Result) { r.touch(id, false) },
		OnUsage:      func(ai.Usage) { r.touch(id, false) },
	}
}

// recordBackground mirrors one background task registry record into the store.
func (r *subagentRecorder) recordBackground(sessionID string, t *agent.BackgroundTask, progress func() ([]ai.Message, ai.Usage, bool)) {
	model, provider, _ := strings.Cut(t.SubModel, " @ ")
	task := session.Task{
		Model: model, Provider: provider,
		ID: t.ID, Description: t.Description, Prompt: t.Prompt,
		Status: string(t.Status), Report: t.Report,
		StartedAt: t.StartedAt, EndedAt: t.EndedAt,
		ParentToolCallID: t.ParentToolCallID,
	}
	if t.Status == agent.TaskRunning {
		r.start(sessionID, task, func() ([]ai.Message, ai.Usage) {
			msgs, usage, _ := progress()
			return msgs, usage
		})
		return
	}
	usage := ai.UsageSummary{Total: t.SubUsage, Models: t.SubModelUsage, Subagents: t.SubSubUsage}
	if _, total, ok := progress(); ok {
		usage.Total = total
	}
	r.finish(sessionID, task, t.SubMessages, usage)
}

// recordingObserver is the turn's request observer: the foreground registry,
// plus saving each foreground subagent it tracks.
type recordingObserver struct {
	*fgRegistry
	rec       *subagentRecorder
	sessionID string
}

func (o recordingObserver) ChildStart(ctx context.Context, info agent.ChildInfo, stop func()) {
	o.fgRegistry.ChildStart(ctx, info, stop)
	t, ok := o.fgRegistry.get(info.ID)
	if !ok {
		return
	}
	toolID := info.ParentToolCallID
	if toolID == "" {
		toolID = tools.ToolCallID(ctx)
	}
	o.rec.start(o.sessionID, session.Task{
		Model: info.Model, Provider: info.Provider,
		ID: info.ID, Description: t.Description, Prompt: info.Prompt,
		Status: string(agent.TaskRunning), StartedAt: t.StartedAt,
		ParentToolCallID: toolID, Foreground: true,
	}, info.Progress)
}

func (o recordingObserver) ChildEnd(ctx context.Context, info agent.ChildInfo, result agent.ChildResult) {
	o.fgRegistry.ChildEnd(ctx, info, result)
	t, ok := o.fgRegistry.get(info.ID)
	if !ok {
		return
	}
	var msgs []ai.Message
	if info.Progress != nil {
		msgs, _ = info.Progress()
	}
	report := t.Report
	if t.Status == agent.TaskDone {
		report = lastAssistantText(msgs)
	}
	usage := ai.UsageSummary{Total: result.Usage}
	o.rec.finish(o.sessionID, session.Task{
		Model: info.Model, Provider: info.Provider,
		ID: info.ID, Description: t.Description, Prompt: info.Prompt,
		Status: string(t.Status), Report: report,
		StartedAt: t.StartedAt, EndedAt: t.EndedAt, Foreground: true,
	}, msgs, usage)
}

func (o recordingObserver) ChildEvents(id string) agent.Events {
	return agent.FanIn(o.fgRegistry.ChildEvents(id), o.rec.events(id))
}

func lastAssistantText(msgs []ai.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "assistant" {
			if text := strings.TrimSpace(msgs[i].TextContent()); text != "" {
				return text
			}
		}
	}
	return ""
}

// subRecorder returns the recorder bound to the current store.
func (m *model) subRecorder() *subagentRecorder {
	if m.subRec == nil || m.subRec.st != m.store {
		m.subRec = newSubagentRecorder(m.store)
	}
	return m.subRec
}

// wireTaskRecords saves background subagents into the current store. It runs
// again once the store opens, which happens after the first agent is wired.
func (m *model) wireTaskRecords() {
	rec := m.subRecorder()
	tasks := m.agent.Tasks()
	tasks.OnRecord = func(sessionID string, t *agent.BackgroundTask) {
		if rec.st == nil || sessionID == "" {
			return
		}
		id := t.ID
		rec.recordBackground(sessionID, t, func() ([]ai.Message, ai.Usage, bool) { return tasks.Progress(id) })
	}
}
