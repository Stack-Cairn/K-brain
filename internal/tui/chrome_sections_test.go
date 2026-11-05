package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// A question card that opens while reply text is still streaming must take
// its rows out of the live area's budget, not out of the transcript's floor.
func TestLiveAreaCapCountsAskDialog(t *testing.T) {
	m := fullModel()
	m.busy = true
	m.Update(askRequest{req: tools.AskRequest{
		Question: "Which output format should --json use?",
		Options:  []tools.AskOption{{Label: "One object per line"}, {Label: "A single array"}},
	}, reply: make(chan askAnswer, 1)})
	m.current = strings.Repeat("streamed words ", 200)
	m.layout()
	view := m.View()

	if m.vp.Height < minTranscriptRows {
		t.Fatalf("transcript squeezed to %d rows with a question open; want at least %d", m.vp.Height, minTranscriptRows)
	}
	if got := lipgloss.Height(view); got > m.height {
		t.Fatalf("frame is %d rows on a %d-row terminal", got, m.height)
	}
	if plain := ansi.Strip(view); !strings.Contains(plain, "Which output format") {
		t.Fatalf("question card not on screen:\n%s", plain)
	}
}

// fixedChrome is the sum of the chrome sections, and layout gives the
// transcript exactly what the sections and the live area leave over.
func TestChromeSectionsAddUp(t *testing.T) {
	m := fullModel()
	m.busy = true
	m.queue = []string{"one", "two"}
	m.quit1 = true
	m.Update(askRequest{req: tools.AskRequest{Question: "Pick one?"}, reply: make(chan askAnswer, 1)})
	m.layout()

	sum := 0
	seen := map[string]bool{}
	for _, s := range m.chromeSections() {
		if seen[s.name] {
			t.Fatalf("duplicate chrome section %q", s.name)
		}
		seen[s.name] = true
		sum += s.rows(m)
	}
	for _, want := range []string{"composer", "busy", "ask", "queue", "quitArm"} {
		if !seen[want] {
			t.Errorf("no %q chrome section", want)
		}
	}
	if got := m.fixedChrome(); got != sum {
		t.Fatalf("fixedChrome = %d, sections add up to %d", got, sum)
	}
	if want := max(m.height-sum, 1); m.vp.Height != want {
		t.Fatalf("viewport height %d, want %d (height %d - chrome %d)", m.vp.Height, want, m.height, sum)
	}
}
