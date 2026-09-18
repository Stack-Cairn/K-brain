package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestToolExpand(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(80, 30))
	result := "line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8"
	m.appendRaw(blockTool, result)

	out := ansi.Strip(m.blocks[0].render(m.width))
	if strings.Contains(out, "line8") || !strings.Contains(out, "… +3 lines") {
		t.Fatalf("collapsed render wrong: %q", out)
	}

	tm, _ := m.key(tea.KeyMsg{Type: tea.KeyCtrlE})
	m = tm.(*model)
	out = ansi.Strip(m.blocks[0].render(m.width))
	if !strings.Contains(out, "line8") || strings.Contains(out, "…") {
		t.Fatalf("expanded render wrong: %q", out)
	}

	tm, _ = m.key(tea.KeyMsg{Type: tea.KeyCtrlE})
	m = tm.(*model)
	if m.blocks[0].expanded {
		t.Fatal("second ctrl+e should collapse")
	}

	m.refreshVP()
	screenY := blockRowY(m, m.blocks[0].y0)
	tm, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 5, Y: screenY})
	m = tm.(*model)
	tm, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: 5, Y: screenY})
	m = tm.(*model)
	if !m.blocks[0].expanded {
		t.Fatalf("click at screen Y=%d should expand the tool block", screenY)
	}
}
