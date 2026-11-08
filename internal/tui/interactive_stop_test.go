package tui

import (
	"context"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// A command run for a background subagent is not stopped by the turn's
// cancel; Ctrl+C twice must still end it, or every key keeps going to it.
func TestCtrlCTwiceStopsTheInteractiveCommand(t *testing.T) {
	m, _ := fragModel(t)
	m.fragFilter = false
	m.cancel = nil // as for a background subagent's command
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	keys := make(chan []byte, 4)
	m.Update(interactiveStartMsg{keys: keys, stop: stop})

	m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	select {
	case b := <-keys:
		if string(b) != "\x03" {
			t.Fatalf("first Ctrl+C sent %q, want ^C", b)
		}
	default:
		t.Fatal("the first Ctrl+C should reach the command")
	}
	if ctx.Err() != nil {
		t.Fatal("one Ctrl+C must not stop the command")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if ctx.Err() == nil {
		t.Fatal("the second Ctrl+C must stop the command")
	}
}
