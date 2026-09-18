package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestChipDisplayNameSanitizesBrackets(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	dir := t.TempDir()
	img := filepath.Join(dir, "a]b.png")
	if err := os.WriteFile(img, []byte("\x89PNG\r\n\x1a\nfake"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := compactCmdModel()
	tm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(img + "\n"), Paste: true})
	m = tm.(*model)
	tm, _ = m.Update(cmd())
	m = tm.(*model)
	chip := m.input.Value()
	if !strings.Contains(chip, "[Image 1: a)b.png"+chipSentinel+"]") {
		t.Errorf("chip should sanitize ] to ), got %q", chip)
	}

	if !strings.Contains(m.expandImageChips(chip), "@"+m.images[0].path) {
		t.Errorf("sanitized chip did not resolve: %q", m.expandImageChips(chip))
	}
}
