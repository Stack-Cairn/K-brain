package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestDegenerateWidthDoesNotStrip(t *testing.T) {
	m := compactCmdModel()
	m.appendAssistant("Here is the assistant reply that must stay readable.")
	m.append(dimStyle.Render("  - Liked the post (815 Likes. Liked)"))

	tm, _ := m.Update(mkWinSize(1, 30))
	m = tm.(*model)

	lines := strings.Split(ansi.Strip(m.vp.View()), "\n")
	var run int
	for _, l := range lines {
		if ansi.StringWidth(strings.TrimRight(l, " ")) == 1 {
			run++
			if run >= 3 {
				t.Fatalf("text collapsed into a one-char-per-line strip at degenerate width (run of %d): %q", run, lines)
			}
		} else {
			run = 0
		}
	}
}

func TestDegenerateWidthThenRealResizeReflows(t *testing.T) {
	m := compactCmdModel()
	m.append(dimStyle.Render("  some tool output line that is long enough to wrap at eighty cols"))

	tm, _ := m.Update(mkWinSize(1, 30))
	m = tm.(*model)
	tm, _ = m.Update(mkWinSize(80, 30))
	m = tm.(*model)

	var maxW int
	for l := range strings.SplitSeq(ansi.Strip(m.vp.View()), "\n") {
		if w := ansi.StringWidth(l); w > maxW {
			maxW = w
		}
	}
	if maxW > 80 {
		t.Fatalf("line exceeds real width after reflow: %d", maxW)
	}
}
