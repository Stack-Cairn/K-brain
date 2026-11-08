package tui

import (
	"context"
	"slices"

	tea "github.com/charmbracelet/bubbletea"
)

// toolBackgroundableMsg says a tool call's shell command became, or stopped
// being, something Ctrl+B can move to the background.
type toolBackgroundableMsg struct {
	id string
	ok bool
}

// toolBackgroundHint is the running row's cue while Ctrl+B can background it.
const toolBackgroundHint = "ctrl+b to background"

// wireBackgrounding reports the agent's backgroundable calls to the program.
// The send is synchronous, so the cue never outlives its call: the call's end
// is reported after it.
func (m *model) wireBackgrounding(send func(tea.Msg)) {
	m.agent.Foreground().SetOnChange(func(id string, ok bool) { send(toolBackgroundableMsg{id: id, ok: ok}) })
}

func (m *model) onToolBackgroundable(msg toolBackgroundableMsg) (tea.Model, tea.Cmd) {
	if m.backgroundable == nil {
		m.backgroundable = map[string]bool{}
	}
	if msg.ok {
		m.backgroundable[msg.id] = true
	} else {
		delete(m.backgroundable, msg.id)
	}
	if m.setBackgroundHint(msg.id, msg.ok) {
		m.refreshVP()
	}
	return m, nil
}

// setBackgroundHint shows or hides the cue on call id's running row.
func (m *model) setBackgroundHint(id string, on bool) bool {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		b := &m.blocks[i]
		if b.kind == blockToolRun && b.toolRunning && b.toolID == id {
			hint := ""
			if on {
				hint = m.tr(toolBackgroundHint)
			}
			if b.bgHint == hint {
				return false
			}
			b.bgHint = hint
			b.stale = true
			return true
		}
	}
	return false
}

// backgroundTools is Ctrl+B while a shell command runs: every running call of
// the main agent that can be moved to the background is moved, and each
// returns at once. It reports false when there is none, so Ctrl+B keeps its
// composer meaning (cursor left).
func (m *model) backgroundTools() (tea.Cmd, bool) {
	if len(m.backgroundable) == 0 {
		return nil, false
	}
	ids := make([]string, 0, len(m.backgroundable))
	for id := range m.backgroundable {
		ids = append(ids, id)
		m.setBackgroundHint(id, false)
	}
	slices.Sort(ids)
	m.backgroundable = nil
	m.refreshVP()
	fg := m.agent.Foreground()
	return func() tea.Msg {
		for _, id := range ids {
			// A call that ended meanwhile is not backgroundable: nothing to do.
			_, _ = fg.Background(context.Background(), id)
		}
		return nil
	}, true
}
