package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// queueEdit is a queued message taken back into the composer for editing.
// It leaves the queue while it is edited and goes back to its old place when
// Enter saves it; Esc puts the original back.
type queueEdit struct {
	idx  int
	orig string
}

// queueRows is the queue's height in the frame: a header row and one row per
// queued message, or 0 when nothing is queued and nothing is being edited.
func (m *model) queueRows() int {
	n := len(m.queue)
	if m.editingQueued() != nil {
		n++
	}
	if n == 0 {
		return 0
	}
	return n + 1
}

// queueFocused reports whether the queue pane has the keys: a turn runs, a
// queued message is selected and the composer is empty.
func (m *model) queueFocused() bool {
	return m.busy && m.queueSel >= 0 && m.queueSel < len(m.queue) && m.input.Value() == "" && m.menu == nil
}

// queueView lists the messages typed while a turn runs, above the composer,
// with the keys that act on them. Every row, the last included, ends in a
// newline.
func (m *model) queueView() string {
	var b strings.Builder
	width := max(m.width, 1)
	edit := m.editingQueued()
	var header string
	var hints []string
	switch {
	case edit != nil:
		header = fmt.Sprintf(m.tr("queued (%d) · editing #%d"), len(m.queue)+1, edit.idx+1)
		hints = []string{m.tr("Enter saves"), m.tr("Esc puts it back")}
	case m.queueFocused():
		header = fmt.Sprintf(m.tr("queued (%d)"), len(m.queue))
		hints = []string{m.tr("Enter send now"), m.tr("Ctrl+E edit"), m.tr("Del remove"), m.tr("↑↓ select"), m.tr("Alt+↑↓ move"), m.tr("Esc back")}
	case m.busy && m.input.Value() == "":
		header = fmt.Sprintf(m.tr("queued (%d)"), len(m.queue))
		hints = []string{m.tr("Enter on empty input sends now"), m.tr("↑ select")}
	default:
		header = fmt.Sprintf(m.tr("queued (%d) · sent when the turn ends"), len(m.queue))
	}
	b.WriteString(dimStyle.Render(fitHints(chromeGlyphs(header), hints, width)) + "\n")

	row := func(i int, q string, selected, editing bool) string {
		num := fmt.Sprintf("%d ", i+1)
		text := strings.Join(strings.Fields(q), " ")
		switch {
		case editing:
			return ansi.Truncate(accentStyle.Render(" "+glyphs.Fold+" ")+dimStyle.Render(num)+dimStyle.Render(text)+dimStyle.Render(m.tr("  (editing)")), width, ellipsisGlyph())
		case selected:
			return selectedRowStyle.Width(width).Render(ansi.Truncate(" "+chromeGlyphs("→")+" "+num+text, width, ellipsisGlyph()))
		default:
			return ansi.Truncate(youStyle.Render(" "+glyphUser)+dimStyle.Render(num)+text, width, ellipsisGlyph())
		}
	}
	shown := 0
	for i, q := range m.queue {
		if edit != nil && edit.idx == shown {
			b.WriteString(row(shown, edit.orig, false, true) + "\n")
			shown++
		}
		b.WriteString(row(shown, q, i == m.queueSel, false) + "\n")
		shown++
	}
	if edit != nil && edit.idx >= shown {
		b.WriteString(row(shown, edit.orig, false, true) + "\n")
	}
	return b.String()
}

// fitHints appends as many " · hint" parts to head as fit in width, in
// order, so the most important keys survive a narrow terminal.
func fitHints(head string, hints []string, width int) string {
	out := head
	for _, h := range hints {
		next := out + chromeGlyphs(" · "+h)
		if ansi.StringWidth(next) > width {
			break
		}
		out = next
	}
	return ansi.Truncate(out, width, ellipsisGlyph())
}

// editingQueued is the queued message being edited, while the turn it was
// queued behind still runs.
func (m *model) editingQueued() *queueEdit {
	if m.cmp == nil || !m.busy {
		return nil
	}
	return m.cmp.queueEdit
}

// enqueue queues a message typed while busy. A message being edited goes back
// to the place it came from; anything else joins the end.
func (m *model) enqueue(text string) {
	c := m.composer()
	if e := c.queueEdit; e != nil {
		c.queueEdit = nil
		at := min(max(e.idx, 0), len(m.queue))
		m.queue = append(m.queue[:at], append([]string{text}, m.queue[at:]...)...)
		return
	}
	m.queue = append(m.queue, text)
}

// queueMove moves the selected message one place up (-1) or down (+1).
func (m *model) queueMove(delta int) {
	i, j := m.queueSel, m.queueSel+delta
	if i < 0 || i >= len(m.queue) || j < 0 || j >= len(m.queue) {
		return
	}
	m.queue[i], m.queue[j] = m.queue[j], m.queue[i]
	m.queueSel = j
}

// queueEditSelected takes the selected message into the composer.
func (m *model) queueEditSelected() {
	i := m.queueSel
	if i < 0 || i >= len(m.queue) {
		return
	}
	text := m.queue[i]
	m.queue = append(m.queue[:i:i], m.queue[i+1:]...)
	m.queueSel = -1
	m.composer().queueEdit = &queueEdit{idx: i, orig: text}
	m.setComposer(text, len([]rune(text)))
	m.composer().resync(m)
}

// queueEditCancel puts the message being edited back unchanged and clears
// the composer.
func (m *model) queueEditCancel() {
	c := m.composer()
	e := c.queueEdit
	if e == nil {
		return
	}
	c.queueEdit = nil
	if m.busy {
		at := min(max(e.idx, 0), len(m.queue))
		m.queue = append(m.queue[:at], append([]string{e.orig}, m.queue[at:]...)...)
		m.input.Reset()
		m.growInput()
	}
	c.resync(m)
}

// queueSendNow interjects the selected message: it moves to the head of the
// queue and the running turn is cancelled, so it is sent as soon as the turn
// winds down.
func (m *model) queueSendNow() (tea.Model, tea.Cmd) {
	i := m.queueSel
	if i < 0 || i >= len(m.queue) {
		return m, nil
	}
	q := m.queue[i]
	m.queue = append([]string{q}, append(m.queue[:i:i], m.queue[i+1:]...)...)
	m.queueSel = -1
	if m.cancel != nil {
		m.cancel()
	}
	return m, nil
}
