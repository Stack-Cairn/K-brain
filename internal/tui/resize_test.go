package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestResizeE2E(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(100, 30))
	m.appendAssistant("Here is a paragraph long enough to wrap differently at different widths, plus a fence.\n\n```go\nfunc example() { fmt.Println(\"" + strings.Repeat("wide", 12) + "\") }\n```")
	m.append(dimStyle.Render("◎ compacted — summarized 42 msgs, 12 kept"))

	vpLines := func() []string {
		return strings.Split(ansi.Strip(m.vp.View()), "\n")
	}
	wide := vpLines()

	tm, _ := m.Update(mkWinSize(48, 30))
	m = tm.(*model)
	narrow := vpLines()
	for _, l := range narrow {
		if w := ansi.StringWidth(l); w > 48 {
			t.Fatalf("viewport line exceeds 48 cols after resize: %q", l)
		}
	}

	if strings.Join(wide, "\n") == strings.Join(narrow, "\n") {
		t.Errorf("viewport content identical after shrink — no reflow")
	}
}
