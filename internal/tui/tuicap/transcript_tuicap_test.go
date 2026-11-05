//go:build tuicap

package tuicap_test

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/tui/tuicap"
)

var paraRE = regexp.MustCompile(`para (\d{3})`)

// topPara is the number of the first paragraph on screen.
func topPara(t *testing.T, screen string) int {
	t.Helper()
	m := paraRE.FindStringSubmatch(screen)
	if m == nil {
		t.Fatalf("no paragraph on screen:\n%s", screen)
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// A long reply streams into the virtualized transcript; scrolled back, the
// window keeps the same paragraph at its top while the terminal narrows (and
// every paragraph rewraps onto two rows) and widens again.
func TestTranscriptScrollAnchorAcrossResize(t *testing.T) {
	const paras = 120
	chunks := make([]string, paras)
	for i := range chunks {
		chunks[i] = fmt.Sprintf("para %03d: a paragraph long enough to wrap once the screen is narrow.\n\n", i+1)
	}
	s := tuicap.Start(t, tuicap.Options{
		Width: 100, Height: 30, Timeout: 20 * time.Second,
		Replies: []tuicap.Reply{{Chunks: chunks, Delay: time.Millisecond}},
	})
	s.WaitFor("Ask k-brain anything")
	s.Type("write a long reply")
	s.Press(tuicap.Enter)
	s.WaitFor(fmt.Sprintf("para %03d", paras))
	s.WaitGone("Esc to interrupt")

	s.Press(tuicap.PgUp, tuicap.PgUp, tuicap.PgUp)
	before := s.WaitGone(fmt.Sprintf("para %03d", paras))
	top := topPara(t, before)
	t.Logf("scrolled back at 100 cols, top paragraph %d:\n%s", top, before)

	s.Resize(60, 30)
	narrow := s.WaitUntil("the transcript rewraps at 60 columns", func(screen string) bool {
		for _, l := range strings.Split(screen, "\n") {
			if strings.HasPrefix(strings.TrimSpace(l), "screen is narrow.") {
				return true
			}
		}
		return false
	})
	if got := topPara(t, narrow); got < top-1 || got > top+1 {
		t.Fatalf("narrowing moved the window from paragraph %d to %d:\n%s", top, got, narrow)
	}
	t.Logf("after narrowing to 60 cols:\n%s", narrow)

	s.Resize(100, 30)
	wide := s.WaitUntil("the transcript unwraps at 100 columns", func(screen string) bool {
		return strings.Contains(screen, "wrap once the screen is narrow.")
	})
	if got := topPara(t, wide); got < top-1 || got > top+1 {
		t.Fatalf("widening moved the window from paragraph %d to %d:\n%s", top, got, wide)
	}

	for range 20 {
		s.Press(tuicap.PgDown)
	}
	s.WaitFor(fmt.Sprintf("para %03d", paras))
}
