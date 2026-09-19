package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func selTestModel() *model {
	m := compactCmdModel()
	m.Update(mkWinSize(80, 30))
	m.append("hello world")
	m.append("second block here")
	tm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")})
	m = tm.(*model)
	m.input.SetValue("")
	return m
}

func blockRowY(m *model, r int) int {
	m.View()
	return m.viewTop + m.vpTopRows() + (r + m.contentPad() - m.vp.YOffset) - m.vpLead
}

func TestDragSelectsHighlightsCopies(t *testing.T) {
	m := selTestModel()
	y := blockRowY(m, m.blocks[1].y0)
	before := m.View()

	tm, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 0, Y: y})
	m = tm.(*model)
	tm, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft, X: 6, Y: y})
	m = tm.(*model)
	if m.sel == nil {
		t.Fatal("motion did not start a selection")
	}
	during := m.View()
	if !strings.Contains(during, "\x1b[7msecond\x1b[27m") {
		t.Fatalf("View must highlight the dragged range:\n%q", during)
	}

	rowOf := func(v string) int {
		for i, l := range strings.Split(v, "\n") {
			if strings.Contains(ansi.Strip(l), "second block here") {
				return i
			}
		}
		return -1
	}
	if rowOf(before) != rowOf(during) {
		t.Fatalf("text shifted during drag: before row %d, during row %d", rowOf(before), rowOf(during))
	}

	tm, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: 6, Y: y})
	m = tm.(*model)
	if m.sel == nil || !m.sel.done {
		t.Fatal("release must keep a done selection for the highlight")
	}
	if got := m.selText(*m.sel); got != "second" {
		t.Fatalf("copied %q, want %q", got, "second")
	}

	after := m.View()
	if rowOf(before) != rowOf(after) {
		t.Fatalf("text shifted after release: before row %d, after row %d", rowOf(before), rowOf(after))
	}

	tm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	m = tm.(*model)
	if m.sel != nil {
		t.Fatal("keypress must clear the selection highlight")
	}
}

func TestClickIsNotASelection(t *testing.T) {
	m := selTestModel()
	y := blockRowY(m, m.blocks[0].y0)
	if handled, _ := m.handleMouseSelect(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 2, Y: y}); !handled {
		t.Fatal("press inside the block range is consumed (the viewport must not scroll on it)")
	}
	if handled, _ := m.handleMouseSelect(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: 2, Y: y}); !handled {
		t.Fatal("release is consumed (it replays the click)")
	}
	if m.sel != nil {
		t.Fatal("a click must leave no selection behind")
	}
}

func TestClickExpandsToolBlock(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(80, 30))
	m.appendRaw(blockTool, "one\ntwo\nthree\nfour\nfive\nsix\nseven")
	m.refreshVP()
	y := blockRowY(m, m.blocks[0].y0)
	tm, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 3, Y: y})
	m = tm.(*model)
	tm, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: 3, Y: y})
	m = tm.(*model)
	if !m.blocks[0].expanded {
		t.Fatal("click must still expand the tool block")
	}
	if m.sel != nil {
		t.Fatal("a click must leave no selection")
	}
}

func TestPressOutsideTranscriptNotConsumed(t *testing.T) {
	m := selTestModel()
	m.View()
	for _, y := range []int{0, 1, m.height - 1} {
		if m.inInputRow(y) {
			continue
		}
		if handled, _ := m.handleMouseSelect(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 2, Y: y}); handled {
			t.Fatalf("press on non-selectable row %d must not be consumed", y)
		}
		if m.sel != nil {
			t.Fatalf("press on non-selectable row %d must not start a selection", y)
		}
	}

	m.handleMouseSelect(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 0, Y: blockRowY(m, m.blocks[0].y0)})
	m.handleMouseSelect(tea.MouseMsg{Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft, X: 80, Y: m.height - 1})
	m.handleMouseSelect(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: 80, Y: m.height - 1})
	if got := m.selText(*m.sel); got != "hello world\n\nsecond block here" {
		t.Fatalf("overshooting drag selected %q", got)
	}
}

func TestDragBackward(t *testing.T) {
	m := selTestModel()
	y := blockRowY(m, m.blocks[1].y0)
	m.handleMouseSelect(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 17, Y: y})
	m.handleMouseSelect(tea.MouseMsg{Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft, X: 7, Y: y})
	m.handleMouseSelect(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: 7, Y: y})
	if got := m.selText(*m.sel); got != "block here" {
		t.Fatalf("backward drag selected %q, want %q", got, "block here")
	}
}

func TestInputDragSelectsHighlightsCopies(t *testing.T) {
	m := selTestModel()
	m.input.SetValue("copy me from input")
	tm, _ := m.Update(mkWinSize(80, 30))
	m = tm.(*model)
	m.View()
	iy := m.inputTop
	if iy < 0 {
		t.Fatal("inputTop must be set after View")
	}
	tm, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 2, Y: iy})
	m = tm.(*model)
	tm, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft, X: 15, Y: iy})
	m = tm.(*model)
	if m.sel == nil || !m.sel.anchor.input {
		t.Fatal("a drag over the input box must start an input-region selection")
	}
	if !strings.Contains(m.View(), "\x1b[7m") {
		t.Fatalf("input selection must paint a highlight:\n%q", m.View())
	}
	tm, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: 15, Y: iy})
	m = tm.(*model)
	if m.sel == nil || !m.sel.done {
		t.Fatal("release must keep a done selection for the highlight")
	}
	if got := m.selText(*m.sel); !strings.Contains(got, "copy me") {
		t.Fatalf("input drag copied %q, want it to contain %q", got, "copy me")
	}
}

func TestInputClickThenType(t *testing.T) {
	m := selTestModel()
	m.input.SetValue("")
	tm, _ := m.Update(mkWinSize(80, 30))
	m = tm.(*model)
	m.View()
	iy := m.inputTop
	tm, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 2, Y: iy})
	m = tm.(*model)
	tm, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: 2, Y: iy})
	m = tm.(*model)
	if m.sel != nil {
		t.Fatal("a no-drag input click must leave no selection")
	}
	tm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")})
	m = tm.(*model)
	if m.input.Value() != "hello" {
		t.Fatalf("typing after an input click broke: input=%q", m.input.Value())
	}
}

func TestDragAcrossBlocks(t *testing.T) {
	m := selTestModel()
	m.handleMouseSelect(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 6, Y: blockRowY(m, m.blocks[0].y0)})
	m.handleMouseSelect(tea.MouseMsg{Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft, X: 6, Y: blockRowY(m, m.blocks[1].y0)})
	m.handleMouseSelect(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: 6, Y: blockRowY(m, m.blocks[1].y0)})
	if got := m.selText(*m.sel); got != "world\n\nsecond" {
		t.Fatalf("cross-block drag selected %q, want %q", got, "world\n\nsecond")
	}
}

func TestCopyKeepsParagraphBreaks(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(80, 30))
	m.append("para one\n\npara two")
	tm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")})
	m = tm.(*model)
	m.input.SetValue("")
	b := m.blocks[0]
	m.handleMouseSelect(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 0, Y: blockRowY(m, b.y0)})
	m.handleMouseSelect(tea.MouseMsg{Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft, X: 8, Y: blockRowY(m, b.y1)})
	m.handleMouseSelect(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: 8, Y: blockRowY(m, b.y1)})
	if got := m.selText(*m.sel); got != "para one\n\npara two" {
		t.Fatalf("paragraph break lost: copied %q", got)
	}
}

func TestContentLineWrapped(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(10, 30))
	m.append("abcdefghij klmnop")
	b := m.blocks[0]
	if got := m.contentLine(b.y0); got != "abcdefghij" {
		t.Fatalf("row 0: got %q", got)
	}
	if got := m.contentLine(b.y0 + 1); got != "klmnop" {
		t.Fatalf("row 1: got %q", got)
	}
}

func TestReverseRange(t *testing.T) {
	if got := reverseRange("hello world", 0, 5); got != "\x1b[7mhello\x1b[27m world" {
		t.Fatalf("got %q", got)
	}
	styled := "\x1b[31mhello\x1b[0m world"
	got := reverseRange(styled, 6, 11)
	if !strings.Contains(got, "\x1b[31mhello\x1b[0m") || !strings.Contains(got, "\x1b[7mworld\x1b[27m") {
		t.Fatalf("styled line mangled: %q", got)
	}

	got = reverseRange("ab\x1b[0mcd", 0, 4)
	if !strings.Contains(got, "\x1b[0m\x1b[7mcd") {
		t.Fatalf("reverse video must be re-asserted after a reset: %q", got)
	}
}

func TestDragEdgeAutoScroll(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(80, 30))
	for i := range 60 {
		m.append(fmt.Sprintf("line-%02d", i))
	}
	tm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")})
	m = tm.(*model)
	m.input.SetValue("")
	m.View()
	if m.vp.YOffset == 0 {
		t.Fatal("test setup: viewport must start scrolled to the bottom")
	}
	start := m.vp.YOffset

	m.handleMouseSelect(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 2, Y: m.viewTop + 5})
	handled, cmd := m.handleMouseSelect(tea.MouseMsg{Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft, X: 2, Y: m.viewTop})
	if !handled || cmd == nil {
		t.Fatalf("edge drag must be handled and arm the scroll tick (handled=%v cmd=%v)", handled, cmd != nil)
	}
	if m.vp.YOffset != start-1 {
		t.Fatalf("edge drag must scroll up one line: %d -> %d", start, m.vp.YOffset)
	}

	for i := 0; i < 200 && m.selEdgeScroll() != nil; i++ {
	}
	if m.vp.YOffset != 0 {
		t.Fatalf("ticks must scroll to the top, YOffset=%d", m.vp.YOffset)
	}
	if m.selEdgeScroll() != nil {
		t.Fatal("at the top the tick must disarm")
	}

	if lo, _ := selOrder(*m.sel); lo.row != m.blocks[0].y0 {
		t.Fatalf("selection must extend to the first content row, got %d", lo.row)
	}

	m.selDragY = m.height - 1
	if m.selEdgeScroll() == nil {
		t.Fatal("drag below the viewport must scroll down")
	}
	if m.vp.YOffset != 1 {
		t.Fatalf("bottom edge must scroll down one line, YOffset=%d", m.vp.YOffset)
	}

	m.handleMouseSelect(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: 2, Y: m.viewTop})
	if m.selEdgeScroll() != nil {
		t.Fatal("after release the tick must disarm")
	}
}

func TestReverseRangeSkipsOSC8Hyperlink(t *testing.T) {
	for _, term := range []string{"\x1b\\", "\a"} {
		line := "at \x1b]8;;file:///tmp/x.md" + term + "/tmp/x.md\x1b]8;;" + term + " ok"
		got := reverseRange(line, 3, 12)
		if s := ansi.Strip(got); s != "at /tmp/x.md ok" {
			t.Fatalf("visible text changed: %q", s)
		}
		if !strings.Contains(got, "\x1b]8;;file:///tmp/x.md"+term) {
			t.Fatalf("hyperlink URI not passed through intact: %q", got)
		}
		if want := "\x1b[7m/tmp/x.md"; !strings.Contains(got, want) {
			t.Fatalf("path not reversed: %q", got)
		}
	}
}
