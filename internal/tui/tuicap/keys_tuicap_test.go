//go:build tuicap

package tuicap_test

import (
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/tui/tuicap"
)

// Ctrl+K and Ctrl+E edit the draft as a shell does — kill to end of line,
// move to end of line — and Ctrl+K no longer clears the conversation.
func TestComposerCtrlKCtrlE(t *testing.T) {
	s := tuicap.Start(t, tuicap.Options{Width: 100, Height: 30, Replies: []tuicap.Reply{tuicap.Text("first answer")}})
	s.WaitFor("Ask k-brain anything")
	s.Type("first question")
	s.Press(tuicap.Enter)
	s.WaitFor("first answer")
	s.WaitGone("Esc to interrupt")

	s.Type("hello brave world")
	s.WaitFor("hello brave world")
	s.Press(tuicap.Ctrl('a'))
	for range len("hello ") {
		s.Press(tuicap.Right)
	}
	s.Press(tuicap.Ctrl('k'))
	screen := s.WaitGone("brave world")
	if !strings.Contains(screen, "first answer") || !strings.Contains(screen, "first question") {
		t.Fatalf("Ctrl+K must not clear the conversation:\n%s", screen)
	}
	t.Logf("after Ctrl+K:\n%s", screen)

	s.Press(tuicap.Ctrl('a'), tuicap.Ctrl('e'))
	s.Type("there")
	s.WaitFor("hello there")
	s.Press(tuicap.Enter)
	s.WaitUntil("the edited prompt is sent", func(string) bool { return len(s.Requests()) >= 2 })
	if reqs := s.Requests(); !reqs[1].Contains("hello there") || reqs[1].Contains("brave") {
		t.Fatalf("the second request should carry the edited draft: %+v", reqs[1])
	}
}

// Text that reaches kn in one write — an input method commit, dictation, a
// laggy remote session — keeps words that spell key names ("up", "home",
// "delete") as words instead of acting on them.
func TestBatchedWordsThatNameKeysAreSentAsTyped(t *testing.T) {
	s := tuicap.Start(t, tuicap.Options{Width: 100, Height: 30, Replies: []tuicap.Reply{tuicap.Text("noted")}})
	s.WaitFor("Ask k-brain anything")
	const text = "a up b x home y 我们 up 吧 delete end"
	s.Type(text)
	s.WaitFor(text)
	s.Press(tuicap.Enter)
	s.WaitFor("noted")
	if reqs := s.Requests(); len(reqs) == 0 || !reqs[0].Contains(text) {
		t.Fatalf("the request should carry the text as typed: %+v", reqs)
	}
}
