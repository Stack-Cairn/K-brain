package tui

import (
	"fmt"
	"sync"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// Approval requests wait their turn for the one permission card. permWait
// counts them, in all and by the subagent that asked, so the card can say how
// many more wait ("+2 pending") and a subagent whose request waits shows
// "waiting for approval" instead of a running clock.
type permWait struct {
	mu     sync.Mutex
	total  int
	byTask map[string]int
}

// permWaitMsg tells the UI the waiting requests changed.
type permWaitMsg struct{}

func (m *model) permWaits() *permWait {
	if m.permWait == nil {
		m.permWait = &permWait{byTask: map[string]int{}}
	}
	return m.permWait
}

func (w *permWait) add(task string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.total++
	if task != "" {
		w.byTask[task]++
	}
}

func (w *permWait) done(task string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.total--
	if task != "" {
		if w.byTask[task]--; w.byTask[task] <= 0 {
			delete(w.byTask, task)
		}
	}
}

func (w *permWait) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.total
}

func (w *permWait) waiting(task string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.byTask[task] > 0
}

// pendingApprovals is how many requests wait behind the open card.
func (m *model) pendingApprovals() int {
	if m.permDialog == nil || m.permWait == nil {
		return 0
	}
	return max(m.permWait.count()-1, 0)
}

// subAwaitingApproval reports whether the subagent id waits for an answer on
// the card: its own request is open, or queued behind the open one. A
// request answered without a card is never shown as waiting.
func (m *model) subAwaitingApproval(id string) bool {
	if m.permDialog == nil {
		return false
	}
	if agent.RequestTaskID(m.permDialog.req.Context) == id {
		return true
	}
	return m.permWait != nil && m.permWait.waiting(id)
}

// permAsker is the subagent a request comes from: "" for the main agent.
func (m *model) permAsker(req tools.GateRequest) string {
	id := agent.RequestTaskID(req.Context)
	if id == "" {
		return ""
	}
	if t, ok := m.subTask(id); ok && t.Description != "" {
		return t.Description
	}
	return id
}

// permAction is what the request does, as the end of "… wants to".
func (m *model) permAction(req tools.GateRequest) string {
	switch {
	case req.Tool == "bash" || req.Tool == "Bash":
		return m.tr("run this command")
	case req.Tool == "web_fetch" || req.Tool == "WebFetch":
		return m.tr("fetch this URL")
	case req.Tool == tools.WebSearchToolName:
		return m.tr("search the web for this")
	case req.Preview != nil && req.Preview.NewFile:
		return m.tr("create this file")
	case req.Tool == "edit" || req.Tool == "Edit":
		return m.tr("apply this edit")
	case req.Tool == "write" || req.Tool == "Write":
		return m.tr("write this file")
	}
	return fmt.Sprintf(m.tr("use %s"), req.Tool)
}
