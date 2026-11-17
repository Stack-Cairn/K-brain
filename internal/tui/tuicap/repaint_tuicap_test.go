//go:build tuicap && !windows

package tuicap_test

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/tui/tuicap"
)

// Output kn did not write — another process sharing the terminal, a
// terminal redrawing its own buffer — can scroll the screen under the
// renderer, which only rewrites the rows that changed: the rule over the
// composer then keeps showing whatever scrolled onto it, often an old info
// row. When the turn ends kn redraws every row, so the screen is whole again.
func TestTurnEndRepaintsAfterOutsideScroll(t *testing.T) {
	lines := make([]string, 30)
	for i := range lines {
		lines[i] = "streamed line"
	}
	s := tuicap.Start(t, tuicap.Options{
		Width: 100, Height: 24, Timeout: 30 * time.Second,
		Replies: []tuicap.Reply{tuicap.Lines(100*time.Millisecond, lines...)},
	})
	s.WaitFor("Ask k-brain anything")
	s.Type("go")
	s.Press(tuicap.Enter)
	s.WaitFor("Esc to interrupt")
	s.Inject("\x1b[24;1H\n\n")
	screen := s.WaitGone("Esc to interrupt")
	screen = s.WaitUntil("a whole screen after the turn", func(screen string) bool {
		return wholeChrome(screen) == ""
	})
	t.Logf("after the turn:\n%s", screen)
	if problem := wholeChrome(screen); problem != "" {
		t.Fatalf("%s:\n%s", problem, screen)
	}
}

// wholeChrome says what is wrong with the composer area, or "" when the rule,
// the composer and one info row sit at the bottom in that order.
func wholeChrome(screen string) string {
	rows := strings.Split(strings.TrimRight(screen, "\n"), "\n")
	if n := infoRows(screen); n != 1 {
		return "the info row is on screen " + strconv.Itoa(n) + " times"
	}
	if len(rows) < 3 {
		return "too few rows"
	}
	rule, composer, info := rows[len(rows)-3], rows[len(rows)-2], rows[len(rows)-1]
	switch {
	case !strings.Contains(rule, "────"):
		return "the row over the composer is not the rule"
	case !strings.Contains(composer, "Ask k-brain anything"):
		return "the composer is not second to last"
	case !strings.Contains(info, "tuicap-model"):
		return "the info row is not last"
	}
	return ""
}

// Ctrl+L redraws every row on demand.
func TestCtrlLRepaints(t *testing.T) {
	s := tuicap.Start(t, tuicap.Options{Width: 100, Height: 24})
	s.WaitFor("Ask k-brain anything")
	s.Inject("\x1b[5;1Hleftover junk")
	s.WaitFor("leftover junk")
	s.Press(tuicap.Ctrl('l'))
	s.WaitGone("leftover junk")
}

func infoRows(screen string) int {
	n := 0
	for _, l := range strings.Split(screen, "\n") {
		if strings.Contains(l, "tuicap-model") {
			n++
		}
	}
	return n
}
