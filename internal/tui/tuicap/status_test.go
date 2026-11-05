//go:build tuicap

package tuicap_test

import (
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/tui/tuicap"
)

// The status row names the phase, counts the streamed tokens and keeps the
// stop key in one row while the turn runs; the footer shows the configured
// status-line command's output; /usage opens over the composer and Esc
// closes it.
func TestStatusRowAndUsage(t *testing.T) {
	lines := make([]string, 12)
	for i := range lines {
		lines[i] = "streamed status line " + strings.Repeat("x", i)
	}
	s := tuicap.Start(t, tuicap.Options{
		Width: 100, Height: 30,
		Replies: []tuicap.Reply{tuicap.Lines(250*time.Millisecond, lines...)},
		Config: map[string]any{
			"statusLine": map[string]any{"command": "echo tuicap-statusline", "intervalMs": 500},
		},
	})
	s.WaitFor("Ask k-brain anything")
	s.WaitFor("tuicap-statusline")

	s.Type("stream a long reply")
	s.Press(tuicap.Enter)
	screen := s.WaitUntil("the status row while writing", func(screen string) bool {
		return strings.Contains(screen, "Writing…") && strings.Contains(screen, "⇣") && strings.Contains(screen, "Esc to interrupt")
	})
	rows := 0
	for _, l := range strings.Split(screen, "\n") {
		if strings.Contains(l, "Esc to interrupt") {
			rows++
			if !strings.Contains(l, "Writing…") {
				t.Errorf("activity and stop key are not one row: %q", l)
			}
		}
	}
	if rows != 1 {
		t.Errorf("status row appears %d times:\n%s", rows, screen)
	}
	t.Logf("status row mid-turn:\n%s", screen)
	s.WaitGone("Esc to interrupt")

	raw := string(s.Raw())
	if !strings.Contains(raw, "\x1b]2;kn · ") {
		t.Errorf("window title never set")
	}

	s.Type("/usage")
	s.Press(tuicap.Esc) // close the completion menu
	s.Press(tuicap.Enter)
	card := s.WaitFor("Esc close")
	for _, want := range []string{"Usage", "Context", "Session", "Cost"} {
		if !strings.Contains(card, want) {
			t.Errorf("/usage lacks %q:\n%s", want, card)
		}
	}
	t.Logf("/usage:\n%s", card)
	s.Press(tuicap.Esc)
	s.WaitGone("Esc close")
	if n := len(s.Requests()); n != 1 {
		t.Fatalf("/usage should not reach the provider; %d requests", n)
	}
}
