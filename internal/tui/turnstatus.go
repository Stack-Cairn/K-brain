package tui

import (
	"strings"
)

// busyRow is the one status row above the composer while a turn runs: the
// spinner and what is happening now on the left (the running tool and how
// long it has been going, or "thinking…"), the turn clock and the stop key on
// the right. It is hidden when idle.
func (m *model) busyRow() string {
	hint := m.tr(" · Esc to interrupt")
	if m.iactive != nil {
		hint = " bash (interactive) — type to respond · Ctrl+C Ctrl+C to cancel"
	} else if m.interrupt1 {
		hint = m.tr(" · Esc again to interrupt")
	}
	left := m.spin.View() + " " + runningStyle.Render(m.busyActivity())
	right := textMutedStyle.Render(strings.TrimPrefix(strings.TrimPrefix(m.busyStats()+hint, " "), "· "))
	if m.width <= 0 {
		return left + " " + right
	}
	return chromeColumns(left, right, m.width)
}

// busyActivity names the newest running tool call, or falls back to thinking.
func (m *model) busyActivity() string {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		b := m.blocks[i]
		if b.kind != blockToolRun || !b.toolRunning || b.toolName == "" {
			continue
		}
		s := toolVerb(b.toolName)
		if subj := rowSubject(b.toolName, b.toolArgs); subj != "" {
			s += " " + truncLine(subj, 48)
		}
		if d, ok := toolTimer.running(b.toolID); ok {
			s += "  " + fmtToolDuration(d)
		}
		return s
	}
	return strings.TrimPrefix(m.tr(" thinking…"), " ")
}
