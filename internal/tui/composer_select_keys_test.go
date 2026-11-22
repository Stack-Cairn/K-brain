package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func shiftKey(t tea.KeyType, alt bool) tea.KeyMsg { return tea.KeyMsg{Type: t, Alt: alt} }

func TestComposerShiftArrowsSelect(t *testing.T) {
	var f fakeClipboard
	useFakeClipboard(t, &f)
	m, _ := composerModel(t, "hello brave world")
	m.placeCursor(len("hello "))
	for range 5 {
		typeKey(m, shiftKey(tea.KeyShiftRight, false))
	}
	if got := m.composerSelText(); got != "brave" {
		t.Fatalf("Shift+→ ×5 selected %q", got)
	}
	if !strings.Contains(m.View(), "\x1b[7m") {
		t.Fatal("a keyboard selection is highlighted like a mouse one")
	}
	// Shift+← shrinks it from the moving end; the anchor stays.
	typeKey(m, shiftKey(tea.KeyShiftLeft, false))
	if got := m.composerSelText(); got != "brav" {
		t.Fatalf("Shift+← should shrink to %q, got %q", "brav", got)
	}
	// Ctrl+C copies it through the clipboard path.
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("Ctrl+C with a selection should copy")
	}
	cmd()
	if len(f.native) != 1 || f.native[0] != "brav" {
		t.Fatalf("copied %q", f.native)
	}
	// Typing replaces it.
	typeKey(m, keyRunes("X"))
	if got := m.input.Value(); got != "hello Xe world" {
		t.Fatalf("typing should replace the selection, got %q", got)
	}
}

func TestComposerShiftHomeEndAndWords(t *testing.T) {
	m, _ := composerModel(t, "one two three\nfour five")
	m.placeCursor(len("one two"))
	typeKey(m, shiftKey(tea.KeyShiftHome, false))
	if got := m.composerSelText(); got != "one two" {
		t.Fatalf("Shift+Home selected %q", got)
	}
	m.clearComposerSel()
	m.placeCursor(len("one "))
	typeKey(m, shiftKey(tea.KeyShiftEnd, false))
	if got := m.composerSelText(); got != "two three" {
		t.Fatalf("Shift+End selected %q", got)
	}

	m.clearComposerSel()
	m.placeCursor(len("one two three\nfour five"))
	typeKey(m, shiftKey(tea.KeyShiftLeft, true))
	if got := m.composerSelText(); got != "five" {
		t.Fatalf("Alt+Shift+← selected %q", got)
	}
	typeKey(m, shiftKey(tea.KeyShiftLeft, true))
	if got := m.composerSelText(); got != "four five" {
		t.Fatalf("a second Alt+Shift+← extends by a word, got %q", got)
	}
	m.clearComposerSel()
	m.placeCursor(0)
	typeKey(m, shiftKey(tea.KeyShiftRight, true))
	typeKey(m, shiftKey(tea.KeyShiftRight, true))
	if got := m.composerSelText(); got != "one two" {
		t.Fatalf("Alt+Shift+→ ×2 selected %q", got)
	}
	// Backspace deletes it.
	typeKey(m, tea.KeyMsg{Type: tea.KeyBackspace})
	if got := m.input.Value(); got != " three\nfour five" {
		t.Fatalf("Backspace should delete the selection, got %q", got)
	}
}

func TestComposerShiftUpDownSelectRows(t *testing.T) {
	m, _ := composerModel(t, "alpha\nbravo\ncharlie")
	m.placeCursor(len("alpha\nbr"))
	typeKey(m, shiftKey(tea.KeyShiftDown, false))
	if got := m.composerSelText(); got != "avo\nch" {
		t.Fatalf("Shift+↓ selected %q", got)
	}
	typeKey(m, shiftKey(tea.KeyShiftDown, false))
	if got := m.composerSelText(); got != "avo\ncharlie" {
		t.Fatalf("Shift+↓ on the last row selects to the end, got %q", got)
	}
	m.clearComposerSel()
	m.placeCursor(len("alpha\nbr"))
	typeKey(m, shiftKey(tea.KeyShiftUp, false))
	if got := m.composerSelText(); got != "pha\nbr" {
		t.Fatalf("Shift+↑ selected %q", got)
	}
	typeKey(m, shiftKey(tea.KeyShiftUp, false))
	if got := m.composerSelText(); got != "alpha\nbr" {
		t.Fatalf("Shift+↑ on the first row selects to the start, got %q", got)
	}
	// A plain arrow collapses the selection.
	typeKey(m, tea.KeyMsg{Type: tea.KeyLeft})
	if _, _, ok := m.composerSelection(); ok {
		t.Fatal("← should drop the selection")
	}
}

// On an empty composer Shift+↑/↓ stay the queue pane's keys.
func TestComposerShiftUpLeavesQueueAlone(t *testing.T) {
	m, _ := composerModel(t, "")
	m.busy = true
	m.queue = []string{"a", "b"}
	m.queueSel = 1
	typeKey(m, shiftKey(tea.KeyShiftUp, false))
	if m.queue[0] != "b" {
		t.Fatalf("Shift+↑ should move the queued message, queue %q", m.queue)
	}
}
