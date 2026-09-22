package tui

import (
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestCtrlKClear(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(80, 30))
	m.appendRaw(blockText, "hello world")
	if got := len(m.agent.Messages); got != 1 {
		t.Fatalf("expected just the system prompt, got %d messages", got)
	}
	m.agent.Messages = append(m.agent.Messages,
		ai.Message{Role: "user", Content: "hi", Authored: true})

	m.input.SetValue("draft")

	tm, _ := m.key(tea.KeyMsg{Type: tea.KeyCtrlK})
	m = tm.(*model)

	if got := len(m.agent.Messages); got != 1 {
		t.Fatalf("ctrl+k should reset messages to the system prompt, got %d", got)
	}

	if m.msgBlock != nil {
		t.Fatal("ctrl+k should drop the pending message block")
	}
	// The banner is reseeded on clear, so the transcript is just the banner:
	// clearing is silent now that the emptied conversation is visible on screen.
	if len(m.blocks) != 1 || m.blocks[0].kind != blockBanner {
		t.Fatalf("expected just the banner, got %d blocks", len(m.blocks))
	}
	if last := m.blocks[len(m.blocks)-1]; strings.Contains(ansi.Strip(last.render(m.width)), "cleared") {
		t.Fatalf("clearing should be silent, got %q", ansi.Strip(last.render(m.width)))
	}
	if m.sessionID != "" {
		t.Fatalf("ctrl+k should detach the session, got %q", m.sessionID)
	}
	if got := m.input.Value(); got != "draft" {
		t.Fatalf("ctrl+k must not delete-after-cursor in the input, got %q", got)
	}
	if out := ansi.Strip(m.View()); strings.Contains(out, "(conversation cleared)") {
		t.Fatalf("clearing should be silent in transcript: %q", out)
	}
}
