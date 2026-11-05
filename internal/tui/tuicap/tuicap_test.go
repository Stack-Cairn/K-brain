//go:build tuicap

package tuicap_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/tui/tuicap"
)

func TestMain(m *testing.M) {
	code := m.Run()
	tuicap.Cleanup()
	os.Exit(code)
}

// kn starts on the composer and a prompt round-trips through the fake
// provider onto the screen.
func TestPromptStreamsReply(t *testing.T) {
	s := tuicap.Start(t, tuicap.Options{
		Replies: []tuicap.Reply{tuicap.Lines(30*time.Millisecond, "first streamed line", "second streamed line")},
	})
	s.WaitFor("Ask k-brain anything")

	s.Type("hello from tuicap")
	s.Press(tuicap.Enter)
	screen := s.WaitFor("second streamed line")
	if !strings.Contains(screen, "first streamed line") {
		t.Fatalf("first line missing:\n%s", screen)
	}
	s.WaitGone("Esc to interrupt") // the turn ends
	reqs := s.Requests()
	if len(reqs) != 1 || !reqs[0].Contains("hello from tuicap") {
		t.Fatalf("provider got %d turns, want one carrying the prompt: %+v", len(reqs), reqs)
	}
	t.Logf("screen after the reply:\n%s", screen)
}

// /help lists the key bindings, generated from the keymap.
func TestHelpShowsKeymap(t *testing.T) {
	s := tuicap.Start(t, tuicap.Options{Width: 160, Height: 50})
	s.WaitFor("Ask k-brain anything")

	s.Type("/help")
	s.Press(tuicap.Esc) // close the completion menu
	s.Press(tuicap.Enter)
	s.WaitFor("Ctrl+K — clear the conversation")
	if n := len(s.Requests()); n != 0 {
		t.Fatalf("/help should not reach the provider, got %d requests", n)
	}
}
