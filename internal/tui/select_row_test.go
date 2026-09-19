package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestSelectionRowAccuracy(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(80, 30))
	m.append("hello world")
	m.appendAssistantBlock("MARKER-ANSWER")
	tm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")})
	m = tm.(*model)
	m.input.SetValue("")

	v := m.View()
	screenRow, screenCol := -1, -1
	for i, l := range strings.Split(v, "\n") {
		if j := strings.Index(ansi.Strip(l), "MARKER-ANSWER"); j >= 0 {

			screenRow, screenCol = i, ansi.StringWidth(ansi.Strip(l)[:j])
		}
	}
	if screenRow < 0 {
		t.Fatal("MARKER not rendered")
	}

	if m.viewTop == 0 {
		t.Fatal("test setup: view must not start at screen row 0")
	}
	t.Logf("MARKER at screen (%d,%d); block[1] y0=%d", screenRow, screenCol, m.blocks[1].y0)

	tm, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: screenCol, Y: screenRow})
	m = tm.(*model)
	tm, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft, X: screenCol + 13, Y: screenRow})
	m = tm.(*model)
	tm, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: screenCol + 13, Y: screenRow})
	m = tm.(*model)
	if m.sel == nil {
		t.Fatal("no selection")
	}
	got := m.selText(*m.sel)
	if !strings.Contains(got, "MARKER") {
		t.Fatalf("drag on row %d copied %q, want MARKER text", screenRow, got)
	}
	t.Logf("copied %q", got)
}
