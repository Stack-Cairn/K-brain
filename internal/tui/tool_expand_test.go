package tui

import (
	"fmt"

	"github.com/Stack-Cairn/K-brain/internal/ai"
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
	if strings.Contains(out, "line8") || !strings.Contains(out, glyphs.Fold+" +5 lines · alt+o") {
		t.Fatalf("collapsed render wrong: %q", out)
	}

	tm, _ := m.key(altKey("o"))
	m = tm.(*model)
	out = ansi.Strip(m.blocks[0].render(m.width))
	if !strings.Contains(out, "line8") || strings.Contains(out, "…") {
		t.Fatalf("expanded render wrong: %q", out)
	}

	tm, _ = m.key(altKey("o"))
	m = tm.(*model)
	if m.blocks[0].expanded {
		t.Fatal("second alt+o should collapse")
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

// Only the block Alt+O toggles names the key on its fold row; a hosted
// search counts as such a block.
func TestFoldHintNamesCtrlEOnlyOnItsTarget(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(80, 30))
	long := "line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8"
	m.appendRaw(blockTool, long)
	m.appendRaw(blockTool, long)
	m.refreshVP()
	first := ansi.Strip(strings.Join(m.blocks[0].linesAt(m.width, false), "\n"))
	last := ansi.Strip(strings.Join(m.blocks[1].linesAt(m.width, false), "\n"))
	if strings.Contains(first, "alt+o") || !strings.Contains(first, glyphs.Fold+" +5 lines") {
		t.Fatalf("an earlier block must not claim alt+o:\n%s", first)
	}
	if !strings.Contains(last, glyphs.Fold+" +5 lines · alt+o") {
		t.Fatalf("the last block names alt+o:\n%s", last)
	}

	var srcs []ai.SearchSource
	for i := range 5 {
		srcs = append(srcs, ai.SearchSource{URL: fmt.Sprintf("https://example.com/%d", i), Title: "t"})
	}
	m.onHostedSearch(ai.HostedSearch{ID: "s1", Status: "done", Queries: []string{"q"}, Sources: srcs})
	search := len(m.blocks) - 1
	last = ansi.Strip(strings.Join(m.blocks[1].linesAt(m.width, false), "\n"))
	got := ansi.Strip(strings.Join(m.blocks[search].linesAt(m.width, false), "\n"))
	if strings.Contains(last, "alt+o") || !strings.Contains(got, "+2 sources · alt+o") {
		t.Fatalf("the hint should move to the search:\ntool:\n%s\nsearch:\n%s", last, got)
	}
	tm, _ := m.key(altKey("o"))
	m = tm.(*model)
	if !m.blocks[search].expanded || m.blocks[1].expanded {
		t.Fatal("alt+o should open the search, the block whose row names it")
	}
}
