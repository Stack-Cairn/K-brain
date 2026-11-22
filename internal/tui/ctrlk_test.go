package tui

import (
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	tea "github.com/charmbracelet/bubbletea"
)

func altKey(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s), Alt: true} }

// Ctrl+K is the shell's kill to end of line: it cuts into the kill ring and
// never clears the conversation, which only /clear does.
func TestCtrlKKillsToLineEnd(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(80, 30))
	m.agent.Messages = append(m.agent.Messages, ai.Message{Role: "user", Content: "hi", Authored: true})
	nBlocks := len(m.blocks)

	m.setComposer("hello brave world", len("hello "))
	tm, _ := m.key(tea.KeyMsg{Type: tea.KeyCtrlK})
	m = tm.(*model)
	if got := m.input.Value(); got != "hello " {
		t.Fatalf("Ctrl+K should cut to the end of the line, got %q", got)
	}
	if got := len(m.agent.Messages); got != 2 {
		t.Fatalf("Ctrl+K must not clear the conversation, %d messages left", got)
	}
	if len(m.blocks) != nBlocks {
		t.Fatalf("Ctrl+K must not touch the transcript: %d blocks, had %d", len(m.blocks), nBlocks)
	}
	tm, _ = m.key(tea.KeyMsg{Type: tea.KeyCtrlY})
	m = tm.(*model)
	if got := m.input.Value(); got != "hello brave world" {
		t.Fatalf("Ctrl+Y should paste the Ctrl+K cut back, got %q", got)
	}

	// Alt+K stays an alias, and kills in a row join one ring entry.
	m.setComposer("one\ntwo", 0)
	m.key(altKey("k"))
	m.key(tea.KeyMsg{Type: tea.KeyCtrlK})
	if got := m.input.Value(); got != "two" {
		t.Fatalf("Alt+K then Ctrl+K should cut the line and its newline, got %q", got)
	}
	if c := m.composer(); c.kills[len(c.kills)-1] != "one\n" {
		t.Fatalf("kills in a row should join, ring top %q", c.kills[len(c.kills)-1])
	}

	m.command("/clear")
	if got := len(m.agent.Messages); got != 1 {
		t.Fatalf("/clear should still reset the conversation, %d messages left", got)
	}
}
