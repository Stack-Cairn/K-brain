package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// A fast transcript reflows on every resize, as it always has.
func TestResizeReflowsAtOnceWhenFast(t *testing.T) {
	m := compactCmdModel()
	// A pinned clock measures every layout as instant; the wall clock would
	// let a loaded machine or the race detector make this transcript slow.
	clock := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	m.now = func() time.Time { return clock }
	m.Update(mkWinSize(100, 30))
	m.seedTranscript(benchSession(2), 1)
	for _, w := range []int{90, 80, 70} {
		_, cmd := m.Update(mkWinSize(w, 30))
		if cmd != nil || m.tx.ix.width != w || m.tx.hold.width != 0 {
			t.Fatalf("width %d: laid out at %d (held %d, cmd %v)", w, m.tx.ix.width, m.tx.hold.width, cmd != nil)
		}
	}
}

// Once a reflow has been slow, a burst of resizes reflows twice: at the
// first resize, and at the last width once the burst stops.
func TestResizeBurstCoalescesWhenSlow(t *testing.T) {
	m := compactCmdModel()
	// The burst happens at one instant: resizes inside it never settle on
	// their own, however slowly the machine runs the test.
	clock := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	m.now = func() time.Time { return clock }
	m.Update(mkWinSize(100, 30))
	m.seedTranscript(benchSession(2), 1)
	slow := func() { m.tx.hold.cost = time.Second }

	slow()
	m.tx.hold.last = time.Time{}
	if _, cmd := m.Update(mkWinSize(90, 30)); cmd != nil || m.tx.ix.width != 90 {
		t.Fatalf("the first resize of a burst must reflow at once: width %d", m.tx.ix.width)
	}
	slow()
	var seqs []int
	for _, w := range []int{85, 80, 75} {
		_, cmd := m.Update(mkWinSize(w, 30))
		if cmd == nil {
			t.Fatalf("width %d: a resize inside the burst must be held", w)
		}
		if m.tx.ix.width != 90 || m.tx.hold.width != 90 {
			t.Fatalf("width %d: held transcript moved to %d", w, m.tx.ix.width)
		}
		seqs = append(seqs, m.tx.hold.seq)
	}
	// Rows wider than the window are cut while held.
	for i, l := range strings.Split(m.View(), "\n") {
		if w := ansi.StringWidth(l); w > 75 {
			t.Fatalf("held frame row %d is %d cells wide in a 75-cell window: %q", i, w, ansi.Strip(l))
		}
	}
	m.Update(reflowSettleMsg{seq: seqs[0]})
	if m.tx.ix.width != 90 {
		t.Fatalf("a stale settle tick reflowed to %d", m.tx.ix.width)
	}
	m.Update(reflowSettleMsg{seq: seqs[len(seqs)-1]})
	if m.tx.ix.width != 75 || m.tx.hold.width != 0 {
		t.Fatalf("after the burst: laid out at %d, held %d; want 75, 0", m.tx.ix.width, m.tx.hold.width)
	}
	// The settled layout is the one a single resize gives.
	want := compactCmdModel()
	want.Update(mkWinSize(75, 30))
	want.seedTranscript(benchSession(2), 1)
	if got, exp := m.View(), want.View(); got != exp {
		t.Fatalf("settled frame differs from a direct layout:\n%s\n---\n%s", got, exp)
	}
}
