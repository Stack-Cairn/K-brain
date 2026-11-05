package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// queueRows is the queue's height in the frame: a header row and one row per
// queued message, or 0 when nothing is queued.
func (m *model) queueRows() int {
	if len(m.queue) == 0 {
		return 0
	}
	return len(m.queue) + 1
}

// queueView lists the messages typed while a turn runs, above the composer.
// Every row, the last included, ends in a newline.
func (m *model) queueView() string {
	var b strings.Builder
	nav := ""
	if m.busy && m.input.Value() == "" {
		nav = " · ↑/↓ Select · Del Removes"
	}
	b.WriteString(dimStyle.Render(fmt.Sprintf(m.tr("queued (%d) · Enter on empty input steers%s"), len(m.queue), nav)) + "\n")
	for i, q := range m.queue {

		line := ansi.Truncate(youStyle.Render(" "+glyphUser)+q, m.width, "…")
		if i == m.queueSel {
			line = ansi.Truncate(botStyle.Render(" → ")+q+dimStyle.Render("  (Del to remove)"), m.width, "…")
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}
