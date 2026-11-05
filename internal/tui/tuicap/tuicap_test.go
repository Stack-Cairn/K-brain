//go:build tuicap

package tuicap_test

import (
	"os"
	"path/filepath"
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

// /help opens the keyboard cheatsheet, generated from the keymap and
// filterable; `/help text` keeps the plain listing.
func TestHelpShowsKeymap(t *testing.T) {
	s := tuicap.Start(t, tuicap.Options{Width: 120, Height: 40})
	s.WaitFor("Ask k-brain anything")

	s.Type("/help")
	s.Press(tuicap.Esc) // close the completion menu
	s.Press(tuicap.Enter)
	s.WaitFor("Keyboard shortcuts")
	s.WaitFor("clear the conversation")
	s.Type("dock")
	screen := s.WaitFor("Subagent dock")
	if strings.Contains(screen, "clear the conversation") {
		t.Fatalf("the filter should hide unrelated keys:\n%s", screen)
	}
	t.Logf("cheatsheet filtered for \"dock\":\n%s", screen)
	s.Press(tuicap.Esc, tuicap.Esc)
	s.WaitFor("Ask k-brain anything")

	s.Type("/help text")
	s.Press(tuicap.Esc)
	s.Press(tuicap.Enter)
	s.WaitFor("Ctrl+K — clear the conversation")
	if n := len(s.Requests()); n != 0 {
		t.Fatalf("/help should not reach the provider, got %d requests", n)
	}
}

func send(s *tuicap.Session, text string) {
	s.Type(text)
	if strings.HasPrefix(text, "/") {
		s.Press(tuicap.Esc) // close the completion menu
	}
	s.Press(tuicap.Enter)
}

// /resume lists earlier sessions; typing filters them, and Ctrl+D deletes
// one after a confirmation.
func TestSessionPickerFilterAndDelete(t *testing.T) {
	s := tuicap.Start(t, tuicap.Options{
		Width: 100, Height: 30,
		Replies: []tuicap.Reply{tuicap.Text("noted the alpha plan"), tuicap.Text("noted the beta checklist")},
	})
	s.WaitFor("Ask k-brain anything")
	send(s, "alpha planning notes")
	s.WaitFor("noted the alpha plan")
	s.WaitGone("Esc to interrupt")
	send(s, "/clear")
	s.WaitGone("noted the alpha plan")
	send(s, "beta release checklist")
	s.WaitFor("noted the beta checklist")
	s.WaitGone("Esc to interrupt")

	send(s, "/resume")
	screen := s.WaitFor("Resume session")
	// The fake provider titles both sessions alike, so only their messages
	// tell them apart: the filter has to search the message text.
	for _, want := range []string{"Resume session", "1/2", "(current)", "beta release checklist"} {
		if !strings.Contains(screen, want) {
			t.Fatalf("picker lacks %q:\n%s", want, screen)
		}
	}
	t.Logf("session picker:\n%s", screen)

	s.Type("alpha")
	screen = s.WaitUntil("the message-text search narrows the list", func(sc string) bool {
		return strings.Contains(sc, "1/1") && strings.Contains(sc, "text match") && strings.Contains(sc, "alpha planning notes")
	})
	t.Logf("filtered for \"alpha\":\n%s", screen)

	s.Press(tuicap.Ctrl('d'))
	screen = s.WaitFor("y to confirm")
	t.Logf("delete confirmation:\n%s", screen)
	s.Type("y")
	screen = s.WaitFor("deleted session")
	if strings.Contains(screen, "alpha planning notes") {
		t.Fatalf("the deleted session is still listed:\n%s", screen)
	}
	s.Press(tuicap.Esc)
	screen = s.WaitFor("1/1")
	if !strings.Contains(screen, "beta release checklist") {
		t.Fatalf("clearing the filter leaves the other session:\n%s", screen)
	}
	s.Press(tuicap.Esc)
	s.WaitFor("Ask k-brain anything")
}

// F2 opens the settings modal from the composer.
func TestSettingsModal(t *testing.T) {
	s := tuicap.Start(t, tuicap.Options{Width: 100, Height: 30})
	s.WaitFor("Ask k-brain anything")
	s.Press("\x1bOQ") // F2
	screen := s.WaitFor("Permission mode")
	t.Logf("settings:\n%s", screen)
	s.Type("permission")
	s.WaitFor("1/1")
	s.Press(tuicap.Right) // Always allow wraps round to Normal
	s.WaitFor("Normal")
	s.Press(tuicap.Esc, tuicap.Esc)
	s.WaitFor("Ask k-brain anything")
}

// An untrusted folder is asked about inside the TUI before anything loads.
func TestOnboardingTrust(t *testing.T) {
	s := tuicap.Start(t, tuicap.Options{Width: 100, Height: 30, Untrusted: true})
	screen := s.WaitFor("Do you trust the files in this folder?")
	if !strings.Contains(screen, "Yes, I trust this folder") {
		t.Fatalf("trust card:\n%s", screen)
	}
	t.Logf("trust question:\n%s", screen)
	s.Press(tuicap.Enter)
	s.WaitFor("Ask k-brain anything")
	data, err := os.ReadFile(filepath.Join(s.Home, "trusted_folders.toml"))
	if err != nil || !strings.Contains(string(data), s.Dir) {
		t.Fatalf("the folder should be trusted: %v\n%s", err, data)
	}
}
