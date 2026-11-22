package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// The busy row names the key that really interrupts: Esc only while Esc
// would do it, Ctrl+C after the first Ctrl+C armed it.
func TestInterruptHintNamesTheRealKey(t *testing.T) {
	m, clock := stClock(t)
	stBegin(m, clock)
	cancelled := 0
	m.cancel = func() { cancelled++ }

	if got := stRow(m); !strings.Contains(got, "Esc to interrupt") {
		t.Fatalf("empty composer: Esc interrupts, row %q", got)
	}

	// A draft takes the first Esc (it arms clearing it), so the row names
	// Ctrl+C Ctrl+C and Esc indeed does not interrupt.
	m.input.SetValue("half a thought")
	if got := stRow(m); !strings.Contains(got, "Ctrl+C Ctrl+C to interrupt") || strings.Contains(got, "Esc to interrupt") {
		t.Fatalf("with a draft: %q", got)
	}
	m.key(tea.KeyMsg{Type: tea.KeyEsc})
	if cancelled != 0 {
		t.Fatal("Esc with a draft must not interrupt")
	}

	// The first Ctrl+C arms the interrupt and says Ctrl+C again, not Esc.
	m.key(tea.KeyMsg{Type: tea.KeyCtrlC})
	if got := stRow(m); !strings.Contains(got, "Ctrl+C again to interrupt") || strings.Contains(got, "Esc again") {
		t.Fatalf("after one Ctrl+C: %q", got)
	}
	if cancelled != 0 {
		t.Fatal("one Ctrl+C must not interrupt")
	}
	m.key(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cancelled != 1 {
		t.Fatalf("the second Ctrl+C should interrupt, cancelled %d", cancelled)
	}

	// On an empty composer one Esc interrupts, as the row said.
	m.interrupt1 = false
	m.input.Reset()
	if got := stRow(m); !strings.Contains(got, "Esc to interrupt") {
		t.Fatalf("empty again: %q", got)
	}
	m.key(tea.KeyMsg{Type: tea.KeyEsc})
	if cancelled != 2 {
		t.Fatalf("Esc on an empty composer should interrupt, cancelled %d", cancelled)
	}
}
