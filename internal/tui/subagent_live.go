package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// Live subagent state shared by the transcript rows, the dock and the subagent
// view: foreground children seen through the turn's request observer, and the
// progress (tool count, latest tool, tokens) of every running subagent.

// subActivity is what a running subagent has done so far.
type subActivity struct {
	tools  int
	latest string
	usage  ai.Usage
}

// subProgressMsg reports one child tool start or one child model response.
type subProgressMsg struct {
	id    string
	tool  string
	usage ai.Usage
}

// subChangedMsg reports a foreground child starting or ending.
type subChangedMsg struct {
	id     string
	toolID string
}

// subTickMsg refreshes elapsed times while a subagent runs.
type subTickMsg struct{}

const subTickInterval = time.Second

func subToolLabel(name, args string) string {
	return truncLine(toolHeaderName(name)+"("+toolSubject(name, args)+")", 48)
}

// subagentBackgroundArg reports whether a subagent tool call asks for a
// background run.
func subagentBackgroundArg(args string) bool {
	var a struct {
		Background bool `json:"background"`
	}
	_ = json.Unmarshal([]byte(args), &a)
	return a.Background
}

// backgroundSpawnID extracts the task id from the subagent tool's
// "Started background subagent <id>…" result.
func backgroundSpawnID(result string) string {
	rest, ok := strings.CutPrefix(result, "Started background subagent ")
	if !ok {
		return ""
	}
	f := strings.Fields(rest)
	if len(f) == 0 {
		return ""
	}
	return strings.TrimSuffix(f[0], ":")
}

const (
	fgJournalBudget    = 256 * 1024
	fgJournalMaxEvents = 2048
)

// fgRegistry tracks foreground subagents. The turn installs it as its request
// observer, so each child started by a foreground subagent tool call reports
// here; the registry keeps a bounded journal for the subagent view.
type fgRegistry struct {
	mu       sync.Mutex
	send     func(tea.Msg)
	calls    map[string]string
	children map[string]*fgChild
	nextSub  int
}

type fgChild struct {
	task      agent.BackgroundTask
	toolID    string
	events    []agent.JournaledEvent
	bytes     int
	truncated bool
	subs      map[int]agent.Events
	stop      func()
}

func newFgRegistry() *fgRegistry {
	return &fgRegistry{calls: map[string]string{}, children: map[string]*fgChild{}}
}

func (r *fgRegistry) emit(msg tea.Msg) {
	r.mu.Lock()
	send := r.send
	r.mu.Unlock()
	if send != nil {
		send(msg)
	}
}

func (r *fgRegistry) setSend(send func(tea.Msg)) {
	r.mu.Lock()
	r.send = send
	r.mu.Unlock()
}

// expect registers a foreground subagent tool call before it runs, so the
// child it starts can be matched to its transcript row.
func (r *fgRegistry) expect(toolID, description string) {
	r.mu.Lock()
	r.calls[toolID] = description
	r.mu.Unlock()
}

// forget drops a call whose child never started (bad arguments, a failed
// worktree, an override error).
func (r *fgRegistry) forget(toolID string) {
	r.mu.Lock()
	delete(r.calls, toolID)
	r.mu.Unlock()
}

func (r *fgRegistry) Start(ctx context.Context, _ agent.RequestObservation) (context.Context, error) {
	return ctx, nil
}

func (r *fgRegistry) End(context.Context, ai.Message, ai.Usage, error) {}

func (r *fgRegistry) Retry(context.Context, ai.RetryEvent) {}

func (r *fgRegistry) ChildStart(ctx context.Context, info agent.ChildInfo, stop func()) {
	id, toolID := info.ID, info.ParentToolCallID
	if toolID == "" {
		toolID = tools.ToolCallID(ctx)
	}
	r.mu.Lock()
	description, ok := r.calls[toolID]
	if ok {
		delete(r.calls, toolID)
		r.children[id] = &fgChild{toolID: toolID, stop: stop, task: agent.BackgroundTask{
			ID: id, Description: description, Prompt: info.Prompt,
			Status: agent.TaskRunning, StartedAt: time.Now(),
		}}
	}
	r.mu.Unlock()
	if ok {
		r.emit(subChangedMsg{id: id, toolID: toolID})
	}
}

func (r *fgRegistry) ChildEnd(ctx context.Context, info agent.ChildInfo, result agent.ChildResult) {
	id, err := info.ID, result.Err
	r.mu.Lock()
	c, ok := r.children[id]
	if ok {
		c.task.Status, c.task.EndedAt = agent.TaskDone, time.Now()
		c.stop = nil
		switch {
		case result.Stopped:
			c.task.Status, c.task.Report = agent.TaskCancelled, err.Error()
		case ctx.Err() != nil:
			c.task.Status, c.task.Report = agent.TaskCancelled, ctx.Err().Error()
		case err != nil:
			c.task.Status, c.task.Report = agent.TaskError, err.Error()
		}
		c.subs = nil
	}
	r.mu.Unlock()
	if ok {
		r.emit(subChangedMsg{id: id, toolID: c.toolID})
	}
}

// stopChild ends one running foreground subagent; the parent turn goes on.
func (r *fgRegistry) stopChild(id string) bool {
	r.mu.Lock()
	c, ok := r.children[id]
	var stop func()
	if ok {
		stop = c.stop
	}
	r.mu.Unlock()
	if stop == nil {
		return false
	}
	stop()
	return true
}

func (r *fgRegistry) ChildEvents(id string) agent.Events {
	r.mu.Lock()
	_, ok := r.children[id]
	r.mu.Unlock()
	if !ok {
		return agent.Events{}
	}
	return agent.Events{
		OnText: func(s string) { r.record(id, 0, s, "") },
		OnToolStart: func(_, n, a string) {
			r.record(id, 1, n, a)
			r.emit(subProgressMsg{id: id, tool: subToolLabel(n, a)})
		},
		OnToolEnd: func(_, n, res string) { r.record(id, 2, n, res) },
		OnSteer:   func(s string) { r.record(id, 3, s, "") },
		OnUsage:   func(u ai.Usage) { r.emit(subProgressMsg{id: id, usage: u}) },
	}
}

func (r *fgRegistry) record(id string, kind int, s, s2 string) {
	r.mu.Lock()
	c, ok := r.children[id]
	if !ok || c.task.Status != agent.TaskRunning {
		r.mu.Unlock()
		return
	}
	n := len(c.events)
	if kind == 0 && n > 0 && c.events[n-1].Kind == 0 && len(c.events[n-1].S) < taskStreamChunkSize {
		c.events[n-1].S += s
	} else {
		c.events = append(c.events, agent.JournaledEvent{Kind: kind, S: s, S2: s2})
	}
	c.bytes += len(s) + len(s2)
	for len(c.events) > 1 && (c.bytes > fgJournalBudget || len(c.events) > fgJournalMaxEvents) {
		c.bytes -= len(c.events[0].S) + len(c.events[0].S2)
		c.events = slices.Delete(c.events, 0, 1)
		c.truncated = true
	}
	subs := make([]agent.Events, 0, len(c.subs))
	for _, ev := range c.subs {
		subs = append(subs, ev)
	}
	r.mu.Unlock()
	for _, ev := range subs {
		switch kind {
		case 0:
			if ev.OnText != nil {
				ev.OnText(s)
			}
		case 1:
			if ev.OnToolStart != nil {
				ev.OnToolStart("", s, s2)
			}
		case 2:
			if ev.OnToolEnd != nil {
				ev.OnToolEnd("", s, s2)
			}
		case 3:
			if ev.OnSteer != nil {
				ev.OnSteer(s)
			}
		}
	}
}

// watch mirrors the background registry's WatchTask for a foreground child.
func (r *fgRegistry) watch(id string, ev agent.Events) (events []agent.JournaledEvent, truncated, live bool, unsubscribe func()) {
	unsubscribe = func() {}
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.children[id]
	if !ok {
		return
	}
	events, truncated = slices.Clone(c.events), c.truncated
	if c.task.Status != agent.TaskRunning {
		return
	}
	live = true
	if c.subs == nil {
		c.subs = map[int]agent.Events{}
	}
	r.nextSub++
	key := r.nextSub
	c.subs[key] = ev
	unsubscribe = sync.OnceFunc(func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if c.subs != nil {
			delete(c.subs, key)
		}
	})
	return
}

func (r *fgRegistry) get(id string) (agent.BackgroundTask, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.children[id]
	if !ok {
		return agent.BackgroundTask{}, false
	}
	return c.task, true
}

func (r *fgRegistry) list() []agent.BackgroundTask {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]agent.BackgroundTask, 0, len(r.children))
	for _, c := range r.children {
		out = append(out, c.task)
	}
	return out
}

func (r *fgRegistry) running() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, c := range r.children {
		if c.task.Status == agent.TaskRunning {
			n++
		}
	}
	return n
}

// clearSettled drops finished children, like the background registry does when
// the user sends a new message.
func (r *fgRegistry) clearSettled(keep ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, c := range r.children {
		if c.task.Status != agent.TaskRunning && !slices.Contains(keep, id) {
			delete(r.children, id)
		}
	}
}

func (m *model) fgReg() *fgRegistry {
	if m.fg == nil {
		m.fg = newFgRegistry()
	}
	return m.fg
}

func (m *model) isForeground(id string) bool {
	_, ok := m.fgReg().get(id)
	return ok
}

// subTask looks a subagent up among background tasks, then foreground children.
func (m *model) subTask(id string) (agent.BackgroundTask, bool) {
	if m.agent != nil {
		if t, ok := m.agent.Tasks().Get(id); ok {
			return t, true
		}
	}
	return m.fgReg().get(id)
}

func (m *model) watchSub(id string, ev agent.Events) ([]agent.JournaledEvent, bool, bool, func()) {
	if m.isForeground(id) {
		return m.fgReg().watch(id, ev)
	}
	return m.agent.Tasks().WatchTask(id, ev)
}

// subUsage is the task's recorded usage, or the live sum while it runs.
func (m *model) subUsage(t agent.BackgroundTask) ai.Usage {
	if taskUsageTokens(t) > 0 {
		return t.SubUsage
	}
	if a := m.subAct[t.ID]; a != nil {
		return a.usage
	}
	return ai.Usage{}
}

func (m *model) subRunning(t agent.BackgroundTask) bool {
	return t.Status == agent.TaskRunning || t.FollowingUp
}

func (m *model) applySubProgress(msg subProgressMsg) {
	if _, ok := m.subTask(msg.id); !ok {
		return
	}
	if m.subAct == nil {
		m.subAct = map[string]*subActivity{}
	}
	a := m.subAct[msg.id]
	if a == nil {
		a = &subActivity{}
		m.subAct[msg.id] = a
	}
	if msg.tool != "" {
		a.tools++
		a.latest = msg.tool
	}
	a.usage.PromptTokens += msg.usage.PromptTokens
	a.usage.CompletionTokens += msg.usage.CompletionTokens
}

// toolCount is "1 tool" or "N tools".
func (m *model) toolCount(n int) string {
	if n == 1 {
		return m.tr("1 tool")
	}
	return fmt.Sprintf(m.tr("%d tools"), n)
}

// subActivityParts is the compact live readout of a subagent: tool count,
// latest tool, elapsed time and tokens, each only when known.
func (m *model) subActivityParts(t agent.BackgroundTask, withLatest bool) []string {
	var parts []string
	a := m.subAct[t.ID]
	if a != nil && a.tools > 0 {
		parts = append(parts, m.toolCount(a.tools))
		if withLatest && m.subRunning(t) && a.latest != "" {
			parts = append(parts, a.latest)
		}
	}
	if elapsed := taskElapsed(t, m.subNow()); elapsed != "" {
		parts = append(parts, elapsed)
	}
	if u := m.subUsage(t); u.PromptTokens+u.CompletionTokens > 0 {
		parts = append(parts, fmtTok(u.PromptTokens+u.CompletionTokens)+" tok")
	}
	return parts
}

func (m *model) subNow() time.Time {
	return m.nowFn()
}

// subagentRowText is the status line under a subagent tool row.
func (m *model) subagentRowText(t agent.BackgroundTask, background bool) string {
	state, style, icon := m.taskState(t)
	head := style.Render(icon + " " + state)
	if background {
		head = style.Render(icon) + " " + t.ID + " " + style.Render(state)
		if m.subRunning(t) {
			head += dimStyle.Render(m.tr(" in background"))
		}
	}
	parts := m.subActivityParts(t, true)
	line := dimStyle.Render("  ⎿ ") + head
	if len(parts) > 0 {
		line += dimStyle.Render(" · " + strings.Join(parts, " · "))
	}
	if m.subRunning(t) {
		line += dimStyle.Render(m.tr(" · Ctrl+T view"))
	}
	return line
}

// refreshSubagentRows rewrites the transcript rows bound to a subagent from its
// current state; only rows whose text changed re-render.
func (m *model) refreshSubagentRows() {
	changed := false
	for i := range m.blocks {
		b := &m.blocks[i]
		if b.subID == "" {
			continue
		}
		t, ok := m.subTask(b.subID)
		if !ok {
			continue
		}
		switch {
		case b.subLine:
			if text := m.subagentRowText(t, true); text != b.text {
				b.text, b.stale, changed = text, true, true
			}
		case b.kind == blockToolRun && b.toolRunning:
			live := ansiStripLine(m.subagentRowText(t, false))
			live = strings.TrimPrefix(live, "  ")
			if live != b.live {
				b.live, b.stale, changed = live, true, true
			}
		case b.kind == blockToolRun:
			text := toolHeaderRow(b.toolName, b.toolArgs, b.toolFailed)
			if !m.subRunning(t) {
				_, style, icon := m.taskState(t)
				text += "  " + style.Render(icon)
				if parts := m.subActivityParts(t, false); len(parts) > 0 {
					text += dimStyle.Render(" " + strings.Join(parts, " · "))
				}
			}
			if text != b.text {
				b.text, b.stale, changed = text, true, true
			}
		}
	}
	if changed {
		m.refreshVP()
	}
}

func (m *model) runningSubagents() int {
	n := m.fgReg().running()
	if m.agent != nil {
		n += m.runningTasks()
	}
	return n
}

func subTick() tea.Cmd {
	return tea.Tick(subTickInterval, func(time.Time) tea.Msg { return subTickMsg{} })
}

// ensureSubTick keeps one ticker alive while any subagent runs.
func (m *model) ensureSubTick() tea.Cmd {
	if m.subTicking || m.runningSubagents() == 0 {
		return nil
	}
	m.subTicking = true
	return subTick()
}

// onSubagentsChanged refreshes everything that shows subagent state after a
// task or foreground child started or settled.
func (m *model) onSubagentsChanged() tea.Cmd {
	for id := range m.subStop {
		if t, ok := m.subTask(id); !ok || !m.subRunning(t) {
			delete(m.subStop, id)
		}
	}
	if m.taskVP != nil {
		if t, ok := m.subTask(m.taskVP.id); ok && !m.subRunning(t) && m.taskVP.live {
			m.openTask(m.taskVP.id)
		} else {
			m.refreshTaskVP()
		}
	}
	m.refreshSubagentRows()
	return m.ensureSubTick()
}

// bindForegroundRow ties the running subagent tool row to the child it started.
func (m *model) bindForegroundRow(msg subChangedMsg) {
	for i := range slices.Backward(m.blocks) {
		b := &m.blocks[i]
		if b.kind == blockToolRun && b.toolID == msg.toolID && b.subID == "" {
			b.subID = msg.id
			return
		}
	}
}

// stopSelectedTask cancels the focused dock row's subagent.
func (m *model) stopSelectedTask() {
	tasks := m.dockList()
	if len(tasks) == 0 {
		return
	}
	t := tasks[min(m.taskSel, len(tasks)-1)]
	switch {
	case !m.subRunning(t):
		m.flashNotice(fmt.Sprintf(m.tr("%s already finished"), t.ID))
	case m.isForeground(t.ID) && m.fgReg().stopChild(t.ID), !m.isForeground(t.ID) && m.agent.Tasks().Cancel(t.ID):
		if m.subStop == nil {
			m.subStop = map[string]bool{}
		}
		m.subStop[t.ID] = true
		m.flashNotice(fmt.Sprintf(m.tr("stopping %s…"), t.ID))
	}
}

func (m *model) flashNotice(text string) {
	m.transientNotice = text
}

// bgNotice is a runtime notice ("[subagent id status] title\n\nbody"), as
// background subagents, workflows and waits deliver them.
type bgNotice struct {
	kind, id, status, title, body string
}

func parseNotice(text string) (bgNotice, bool) {
	head, body, _ := strings.Cut(text, "\n")
	if !strings.HasPrefix(head, "[") {
		return bgNotice{}, false
	}
	end := strings.IndexByte(head, ']')
	if end < 0 {
		return bgNotice{}, false
	}
	f := strings.Fields(head[1:end])
	if len(f) < 2 || len(f) > 3 {
		return bgNotice{}, false
	}
	switch f[0] {
	case "subagent", "workflow", "wait":
	default:
		return bgNotice{}, false
	}
	n := bgNotice{kind: f[0], id: f[1], title: strings.TrimSpace(head[end+1:]), body: strings.Trim(body, "\n")}
	if len(f) == 3 {
		n.status = f[2]
	}
	return n, true
}

func (m *model) noticeStyle(status string) lipgloss.Style {
	switch status {
	case "done", "complete", "completed":
		return growStyle
	case "cancelled", "killed":
		return dimStyle
	case "running":
		return accentStyle
	}
	return errStyle
}

// noticeBlocks renders a runtime notice as a quiet status row plus a folded
// body, never as something the user typed.
func (m *model) noticeBlocks(text string) []block {
	n, ok := parseNotice(text)
	if !ok {
		return []block{{kind: blockText, text: dimStyle.Render("◆ " + firstLine(text))}}
	}
	glyph := "◆"
	if n.kind == "wait" {
		glyph = "⏲"
	}
	status := n.status
	if status == "" {
		status = "error"
	}
	style := m.noticeStyle(status)
	head := accentStyle.Render(glyph) + " " + dimStyle.Render(m.tr(n.kind)) + " " + n.id + " " + style.Render(m.tr(status))
	if n.title != "" {
		head += dimStyle.Render(" · " + truncLine(n.title, 80))
	}
	if n.kind == "subagent" {
		if t, ok := m.subTask(n.id); ok {
			if parts := m.subActivityParts(t, false); len(parts) > 0 {
				head += dimStyle.Render(" · " + strings.Join(parts, " · "))
			}
		}
	}
	out := []block{{kind: blockText, text: head}}
	if strings.TrimSpace(n.body) != "" {
		out = append(out, block{kind: blockTool, text: n.body, toolName: "notice"})
	}
	return out
}

func (m *model) appendNotice(text string) {
	m.keepFollow()
	m.blocks = append(m.blocks, m.noticeBlocks(text)...)
	m.refreshVP()
}

func ansiStripLine(s string) string {
	return ansi.Strip(s)
}
