//go:build tuicap && !windows

package tuicap_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/tui/tuicap"
)

// altScreenSeqs are the ways into the alternate screen.
var altScreenSeqs = []string{"\x1b[?1049h", "\x1b[?1047h", "\x1b[?47h"}

// inlineTurn sends prompt and waits for the reply's last line and the end of
// the turn.
func inlineTurn(s *tuicap.Session, prompt, last string) {
	s.Type(prompt)
	s.Press(tuicap.Enter)
	s.WaitFor(last)
	s.WaitGone("Esc to interrupt")
}

// count is how many rows of screen contain text.
func count(screen, text string) int {
	n := 0
	for _, l := range strings.Split(screen, "\n") {
		if strings.Contains(l, text) {
			n++
		}
	}
	return n
}

// rowOf is the index of the first row containing text, or -1.
func rowOf(screen, text string) int {
	for i, l := range strings.Split(screen, "\n") {
		if strings.Contains(l, text) {
			return i
		}
	}
	return -1
}

// In inline mode a finished reply is printed above the live region, kn never
// switches to the alternate screen, and the conversation is still in the
// terminal after kn exits.
func TestInlineReplyStaysInScrollback(t *testing.T) {
	s := tuicap.Start(t, tuicap.Options{
		Args: []string{"--inline"},
		Replies: []tuicap.Reply{
			tuicap.Lines(20*time.Millisecond, "inline reply alpha", "inline reply omega"),
		},
	})
	s.WaitFor("Ask k-brain anything")
	inlineTurn(s, "first inline prompt", "inline reply omega")

	screen := s.WaitUntil("the reply printed above the composer", func(screen string) bool {
		reply, composer := rowOf(screen, "inline reply omega"), rowOf(screen, "Ask k-brain anything")
		return reply >= 0 && composer > reply
	})
	t.Logf("after the turn:\n%s", screen)
	if s.AltScreen() {
		t.Fatal("inline mode is on the alternate screen")
	}

	s.Press(tuicap.CtrlC, tuicap.CtrlC)
	s.WaitExit()
	history := s.History()
	t.Logf("terminal after exit:\n%s", history)
	for _, text := range []string{"first inline prompt", "inline reply alpha", "inline reply omega"} {
		if n := count(history, text); n != 1 {
			t.Fatalf("%q is in the terminal %d times after exit, want once", text, n)
		}
	}
	if rowOf(history, "inline reply omega") > rowOf(history, "resume with") {
		t.Fatal("the reply should sit above kn's exit line")
	}
	if count(history, "Ask k-brain anything") != 0 {
		t.Fatal("the live region should be cleared on exit")
	}
	for _, seq := range altScreenSeqs {
		if bytes.Contains(s.Raw(), []byte(seq)) {
			t.Fatalf("inline mode wrote %q, which enters the alternate screen", seq)
		}
	}
}

// A resize redraws the live region only: printed history is never printed
// again, whichever way the terminal changes size.
func TestInlineResizeKeepsHistoryOnce(t *testing.T) {
	var replies []tuicap.Reply
	for i := range 4 {
		lines := make([]string, 8)
		for j := range lines {
			lines[j] = fmt.Sprintf("reply %d line %d", i, j)
		}
		replies = append(replies, tuicap.Lines(5*time.Millisecond, lines...))
	}
	s := tuicap.Start(t, tuicap.Options{Args: []string{"--inline"}, Width: 90, Height: 20, Replies: replies, Timeout: 30 * time.Second})
	s.WaitFor("Ask k-brain anything")
	for i := range 4 {
		inlineTurn(s, fmt.Sprintf("prompt number %d", i), fmt.Sprintf("reply %d line 7", i))
	}
	s.WaitUntil("the last reply printed", func(string) bool {
		return count(s.History(), "reply 3 line 7") == 1 && count(s.Snapshot(), "Ask k-brain anything") == 1
	})
	if count(s.History(), "reply 0 line 0") != 1 {
		t.Fatalf("the first reply should be in the scrollback once:\n%s", s.History())
	}

	for _, size := range [][2]int{{70, 16}, {110, 28}, {90, 20}} {
		s.Resize(size[0], size[1])
		time.Sleep(300 * time.Millisecond)
		s.WaitFor("Ask k-brain anything")
	}
	s.Press(tuicap.Ctrl('l'))
	time.Sleep(200 * time.Millisecond)
	history := s.History()
	for i := range 4 {
		for _, text := range []string{fmt.Sprintf("prompt number %d", i), fmt.Sprintf("reply %d line 0", i), fmt.Sprintf("reply %d line 7", i)} {
			if n := count(history, text); n != 1 {
				t.Fatalf("%q is in the terminal %d times after resizing, want once:\n%s", text, n, history)
			}
		}
	}
	if n := count(s.Snapshot(), "Ask k-brain anything"); n != 1 {
		t.Fatalf("the composer is on screen %d times:\n%s", n, s.Snapshot())
	}
}

// A view that needs the whole screen borrows the alternate screen and gives
// it back; the transcript printed before is untouched.
func TestInlineFullScreenViewUsesAltScreen(t *testing.T) {
	s := tuicap.Start(t, tuicap.Options{
		Args:    []string{"--inline"},
		Width:   100,
		Height:  30,
		Replies: []tuicap.Reply{tuicap.Text("inline settings reply")},
	})
	s.WaitFor("Ask k-brain anything")
	inlineTurn(s, "before settings", "inline settings reply")

	s.Type("/settings")
	s.Press(tuicap.Esc)
	s.Press(tuicap.Enter)
	s.WaitFor("Inline mode")
	if !s.AltScreen() {
		t.Fatal("/settings should open on the alternate screen")
	}
	s.Press(tuicap.Esc)
	s.WaitFor("Ask k-brain anything")
	s.WaitUntil("back on the normal screen", func(string) bool { return !s.AltScreen() })
	if n := count(s.History(), "inline settings reply"); n != 1 {
		t.Fatalf("the reply is in the terminal %d times after /settings, want once:\n%s", n, s.History())
	}
}
