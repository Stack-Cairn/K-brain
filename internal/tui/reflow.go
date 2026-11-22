package tui

// Resize coalescing.
//
// A width change reflows every block of the transcript, which on a long
// session takes a good fraction of a second. Dragging a terminal edge sends
// a burst of resizes, and reflowing for each one queues seconds of work
// behind the drag. So once a reflow has proved slow, a resize that follows
// another within resizeSettle keeps the transcript laid out at the width it
// has (rows wider than the window are cut, as always) and reflows once, at
// the latest width, when the resizes stop. The first resize of a burst
// still reflows at once, and a transcript that reflows fast is never held.

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const (
	// reflowSlow is how long a whole-transcript layout must have taken
	// before resizes coalesce.
	reflowSlow = 60 * time.Millisecond
	// resizeSettle is how long the resizes must stop for before the held
	// transcript reflows.
	resizeSettle = 120 * time.Millisecond
)

// reflowSettleMsg ends a held reflow; seq is the resize it was scheduled
// for, and only the latest one counts.
type reflowSettleMsg struct{ seq int }

// reflowHold is the transcript's resize state.
type reflowHold struct {
	// cost is how long the last layout of the whole transcript took.
	cost time.Duration
	// width is the width the transcript stays laid out at while held; 0
	// when not held.
	width int
	// seq numbers held resizes; last is when the last resize was handled.
	seq  int
	last time.Time
}

// layoutWidth is the width refreshVP lays the transcript out at.
func (m *model) layoutWidth() int {
	if m.tx != nil && m.tx.hold.width > 0 {
		return m.tx.hold.width
	}
	return max(m.width, minRenderWidth)
}

// onResizeReflow reflows the transcript for a width change, or holds it
// and returns the tick that will.
func (m *model) onResizeReflow() tea.Cmd {
	tx := m.txs()
	h := &tx.hold
	now := time.Now()
	if h.width == 0 && (h.cost < reflowSlow || now.Sub(h.last) >= resizeSettle || !tx.ix.valid) {
		m.refreshVP()
		h.last = time.Now()
		return nil
	}
	if h.width == 0 {
		h.width = tx.ix.width
	}
	h.seq++
	h.last = now
	seq := h.seq
	return tea.Tick(resizeSettle, func(time.Time) tea.Msg { return reflowSettleMsg{seq: seq} })
}

// onReflowSettle lays a held transcript out at the current width once the
// resizes have stopped.
func (m *model) onReflowSettle(msg reflowSettleMsg) {
	if m.tx == nil || m.tx.hold.width == 0 || msg.seq != m.tx.hold.seq {
		return
	}
	m.tx.hold.width = 0
	m.refreshVP()
	m.tx.hold.last = time.Now()
}
