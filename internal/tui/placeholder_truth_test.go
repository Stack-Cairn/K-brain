package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// placeholderAfter runs the model once and returns the empty composer's
// placeholder as drawn.
func placeholderAfter(m *model) string {
	m.View()
	return m.input.Placeholder
}

// The busy placeholder says when a queued message goes in: after the running
// tool call only while one runs, after the reply while the model writes, and
// not at all while an approval card holds the keys.
func TestBusyPlaceholderMatchesTheTurnPhase(t *testing.T) {
	m, clock := stClock(t)
	m.Update(mkWinSize(120, 30))
	stBegin(m, clock)
	m.Update(phaseMsg(phaseWaiting))
	if got := placeholderAfter(m); strings.Contains(got, "tool call") || !strings.Contains(got, "after this reply") {
		t.Fatalf("streaming, no tool running: %q", got)
	}
	id := stTool("ph")
	m.Update(toolStartMsg{id: id, name: "bash", args: `{"command":"sleep 5"}`})
	if got := placeholderAfter(m); !strings.Contains(got, "after the current tool call") {
		t.Fatalf("tool running: %q", got)
	}
	m.Update(toolEndMsg{id: id, name: "bash", result: "ok"})
	if got := placeholderAfter(m); strings.Contains(got, "tool call") {
		t.Fatalf("tool finished: %q", got)
	}
	m.permDialog = &permDialog{req: tools.GateRequest{Tool: "bash", Command: "rm x"}, reply: make(chan permAnswer, 1)}
	if got := placeholderAfter(m); strings.Contains(got, "queue") || !strings.Contains(got, "answer above") {
		t.Fatalf("approval card open: %q", got)
	}
}

// The "draft stashed" hint follows a stash: it goes once the draft is
// restored or a message is sent, instead of staying for the session.
func TestStashPlaceholderGoesAfterRestoreOrSend(t *testing.T) {
	m, _ := stClock(t)
	m.Update(mkWinSize(120, 30))
	m.queueSel = -1
	key := func(k tea.KeyMsg) {
		tm, _ := m.Update(k)
		m = tm.(*model)
	}
	m = typeStr(t, m, "first draft")
	key(tea.KeyMsg{Type: tea.KeyCtrlS})
	if got := placeholderAfter(m); !strings.Contains(got, "draft stashed") {
		t.Fatalf("after stashing: %q", got)
	}
	key(tea.KeyMsg{Type: tea.KeyCtrlS}) // restore
	if m.input.Value() != "first draft" {
		t.Fatalf("restore: %q", m.input.Value())
	}
	for range len("first draft") {
		key(tea.KeyMsg{Type: tea.KeyBackspace})
	}
	if got := placeholderAfter(m); strings.Contains(got, "stashed") {
		t.Fatalf("restored then cleared, stash empty: %q", got)
	}

	// A stashed draft is announced until the composer holds a draft again:
	// sending the next message does not leave the hint up for the session.
	m = typeStr(t, m, "second draft")
	key(tea.KeyMsg{Type: tea.KeyCtrlS})
	if got := placeholderAfter(m); !strings.Contains(got, "draft stashed") {
		t.Fatalf("after the second stash: %q", got)
	}
	// Esc Esc also keeps a cleared draft in the stash.
	m = typeStr(t, m, "third draft")
	key(tea.KeyMsg{Type: tea.KeyEsc})
	key(tea.KeyMsg{Type: tea.KeyEsc})
	if m.input.Value() != "" {
		t.Fatalf("esc esc did not clear: %q", m.input.Value())
	}
	m = typeStr(t, m, "hello")
	key(tea.KeyMsg{Type: tea.KeyEnter})
	m.busy = false
	if got := placeholderAfter(m); strings.Contains(got, "stashed") {
		t.Fatalf("after sending a message: %q", got)
	}
	if len(m.cmp.stash) != 2 {
		t.Fatalf("the stashed draft itself stays restorable: %q", m.cmp.stash)
	}
}
