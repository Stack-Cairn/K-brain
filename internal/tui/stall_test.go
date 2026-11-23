package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

// A provider that sends nothing for the watchdog's two minutes is retried
// with a readout that says so, and if it never answers the turn ends with a
// "no response" banner, never the "(interrupted …)" line a user's Esc gets.
func TestStallIsShownAsNoResponseNotInterrupt(t *testing.T) {
	m, clock := stClock(t)
	m.provName = "fake"
	stBegin(m, clock)
	stall := ai.StreamStallError{Idle: 2 * time.Minute, Provider: "OpenAI"}
	m.Update(retryMsg{Attempt: 1, Max: 8, Delay: 2 * time.Second, Err: stall})
	if got := stRow(m); !strings.Contains(got, "No response from fake for 2m — retrying in 2s (2/8)") || !strings.Contains(got, "Esc") {
		t.Fatalf("stall retry readout: %q", got)
	}
	m.Update(turnDoneMsg{err: fmt.Errorf("turn: %w", stall)})
	m.Update(mkWinSize(100, 40))
	view := strings.Join(strings.Fields(ansi.Strip(m.View())), " ")
	if strings.Contains(view, "interrupted") {
		t.Fatalf("a stall must not read as an interrupt:\n%s", view)
	}
	for _, want := range []string{"No response from the model", "No response from fake for 2m."} {
		if !strings.Contains(view, want) {
			t.Errorf("missing %q in\n%s", want, view)
		}
	}
}
