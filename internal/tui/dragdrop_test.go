package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestFinderDragImagePathDetected(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "Screenshot 2026-09-04 at 3.21.45 PM.png")
	if err := os.WriteFile(img, []byte("\x89PNG\r\n\x1a\nfake"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := tasksModel("http://unused")
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	tm, cmd := m.Update(dragRunes(img))
	m = tm.(*model)
	if cmd == nil {
		t.Fatal("a dragged image path should schedule an attachment command")
	}
	tm, _ = m.Update(cmd())
	m = tm.(*model)
	if len(m.images) != 1 {
		t.Fatalf("Finder drag produced %d images, want 1", len(m.images))
	}
	if !strings.Contains(m.input.Value(), "[Image 1: Screenshot") || !strings.Contains(m.input.Value(), ".png"+chipSentinel+"]") {
		t.Errorf("input = %q, want the [Image 1: <name>…png] chip", m.input.Value())
	}
}

func TestSingleRuneKeyNotADrag(t *testing.T) {
	m := tasksModel("http://unused")
	um, _ := m.Update(dragRunes("a"))
	m = um.(*model)
	if len(m.images) != 0 {
		t.Fatalf("single rune produced %d images, want 0", len(m.images))
	}
}

func dragRunes(path string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(path), Paste: false}
}
