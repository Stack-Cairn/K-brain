package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func renderedTextPosition(t *testing.T, view, text string) (int, int) {
	t.Helper()
	for row, line := range strings.Split(ansi.Strip(view), "\n") {
		if col := strings.Index(line, text); col >= 0 {
			return ansi.StringWidth(line[:col]), row
		}
	}
	t.Fatalf("text %q not rendered:\n%s", text, ansi.Strip(view))
	return 0, 0
}

func TestSelectionUsesRenderedCoordinates(t *testing.T) {
	for _, height := range []int{12, 24, 30, 50} {
		for _, scrolled := range []bool{false, true} {
			for _, input := range []bool{false, true} {
				t.Run(fmt.Sprintf("height=%d/scrolled=%v/input=%v", height, scrolled, input), func(t *testing.T) {
					m := compactCmdModel()
					m.Update(mkWinSize(80, height))
					if scrolled {
						for i := range 60 {
							m.append(fmt.Sprintf("history %d", i))
						}
					}
					text := "中文 selection"
					if input {
						m.append("transcript must not be highlighted")
						m.input.SetValue("prefix " + text + " suffix")
					} else {
						m.appendAssistantBlock("prefix " + text + " suffix")
					}
					m.layout()
					before := m.View()
					x, y := renderedTextPosition(t, before, text)
					blocks, offset := len(m.blocks), m.vp.YOffset
					m.handleMouseSelect(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: x, Y: y})
					m.handleMouseSelect(tea.MouseMsg{Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft, X: x + ansi.StringWidth(text), Y: y})
					if m.sel == nil || m.sel.anchor.input != input {
						t.Fatalf("wrong selection region: %+v", m.sel)
					}
					if got := m.selText(*m.sel); got != text {
						t.Fatalf("selected %q, want %q", got, text)
					}
					during := m.View()
					if ansi.Strip(during) != ansi.Strip(before) {
						t.Fatal("drag changed visible text or layout")
					}
					if !strings.Contains(during, "\x1b[7m") {
						t.Fatal("selection is not highlighted")
					}
					if input && strings.Contains(m.viewportView(), "\x1b[7m") {
						t.Fatal("input selection highlighted transcript")
					}
					_, cmd := m.handleMouseSelect(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: x + ansi.StringWidth(text), Y: y})
					if cmd == nil || len(m.blocks) != blocks || m.vp.YOffset != offset {
						t.Fatal("copy must show a transient notice without modifying the transcript")
					}
					if _, afterY := renderedTextPosition(t, m.View(), text); afterY != y {
						t.Fatalf("copy moved selected text: %d -> %d", y, afterY)
					}
					m.Update(noticeExpiredMsg(m.noticeGeneration))
					if ansi.Strip(m.View()) != ansi.Strip(before) {
						t.Fatal("expired notice changed the layout")
					}
				})
			}
		}
	}
}

func TestSelectionRejectsChromeWhenScrolled(t *testing.T) {
	m := selTestModel()
	for i := range 60 {
		m.append(fmt.Sprintf("history %d", i))
	}
	m.vp.SetYOffset(m.vp.YOffset / 2)
	m.follow = false
	m.View()
	for y := range m.height {
		if y >= max(m.viewportTop, 0) && y < m.viewportTop+m.viewportRows {
			continue
		}
		if _, ok := m.selPoint(4, y, false); ok {
			t.Fatalf("non-transcript row %d is selectable", y)
		}
	}
}

func TestTransientNoticeExpiresOnlyMatchingGeneration(t *testing.T) {
	m := selTestModel()
	m.sessTitle = "Session title"
	before := ansi.Strip(m.View())
	m.showNotice("first")
	old := m.noticeGeneration
	m.showNotice("second")
	m.Update(noticeExpiredMsg(old))
	if m.transientNotice != "second" {
		t.Fatal("old timer cleared a newer notice")
	}
	view := m.View()
	if !strings.Contains(view, "second") || !strings.Contains(view, "Session title") {
		t.Fatal("notice must leave the session title visible")
	}
	m.Update(noticeExpiredMsg(m.noticeGeneration))
	if m.transientNotice != "" || ansi.Strip(m.View()) != before {
		t.Fatal("notice did not expire without changing the layout")
	}
}
