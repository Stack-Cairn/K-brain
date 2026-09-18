package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func newGrowModel() *model {
	m := &model{input: newInput(), now: time.Now}
	m.width = 80
	m.input.SetWidth(m.width - 2)
	m.layout()
	return m
}

func TestInputGrowsOnCtrlJ(t *testing.T) {
	m := newGrowModel()
	if got := m.input.Height(); got != 1 {
		t.Fatalf("empty input should be 1 line, got %d", got)
	}

	m.input.SetValue("first line")
	m.input.CursorEnd()

	tm, _ := m.key(tea.KeyMsg{Type: tea.KeyCtrlJ})
	m = tm.(*model)
	m.input.InsertString("second line")
	m.layout()

	if got := m.input.LineCount(); got != 2 {
		t.Fatalf("ctrl+j should insert a newline: LineCount=%d value=%q", got, m.input.Value())
	}
	if got := m.input.Height(); got != 2 {
		t.Fatalf("input box should grow to 2 lines, got %d", got)
	}

	tm, _ = m.key(tea.KeyMsg{Type: tea.KeyCtrlJ})
	m = tm.(*model)
	m.input.InsertString("third line")
	m.layout()
	if got := m.input.Height(); got != 3 {
		t.Fatalf("input box should grow to 3 lines, got %d", got)
	}
}

func TestInputGrowsOnWrap(t *testing.T) {
	m := newGrowModel()
	m.input.SetValue(strings.Repeat("x", (m.input.Width()-2)*2))
	m.layout()
	if got := m.input.Height(); got != 2 {
		t.Fatalf("wrapped long line should need 2 rows, got %d", got)
	}
}

func TestInputCappedAtMaxHeight(t *testing.T) {
	m := newGrowModel()
	var lines []string
	for range 50 {
		lines = append(lines, "line")
	}
	m.input.SetValue(strings.Join(lines, "\n"))
	m.layout()
	if got := m.input.Height(); got != m.input.MaxHeight {
		t.Fatalf("input should cap at MaxHeight=%d, got %d", m.input.MaxHeight, got)
	}
}

func TestInputShrinksWhenContentRemoved(t *testing.T) {
	m := newGrowModel()
	m.input.SetValue("a\nb\nc")
	m.layout()
	if got := m.input.Height(); got != 3 {
		t.Fatalf("3 lines, got %d", got)
	}
	m.input.SetValue("a")
	m.layout()
	if got := m.input.Height(); got != 1 {
		t.Fatalf("should shrink back to 1 line, got %d", got)
	}
}

func TestInputShowsAllLinesAfterGrowth(t *testing.T) {
	m := newGrowModel()
	lines := []string{"line one", "line two", "line three", "line four"}
	for i, ln := range lines {
		if i > 0 {
			tm, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
			m = tm.(*model)
		}
		for _, r := range ln {
			tm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
			m = tm.(*model)
		}
	}
	if got := m.input.Height(); got != len(lines) {
		t.Fatalf("box should have grown to %d lines, got %d", len(lines), got)
	}
	rendered := m.input.View()
	for _, ln := range lines {
		if !strings.Contains(rendered, ln) {
			t.Errorf("rendered input is missing %q\n--- rendered ---\n%s", ln, rendered)
		}
	}
}

func TestCtrlJWorksAfterLargePaste(t *testing.T) {
	m := newGrowModel()
	var lines []string
	for i := range m.input.MaxHeight + 5 {
		lines = append(lines, fmt.Sprintf("pasted %d", i))
	}

	block := strings.Join(lines, "\n")
	tm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(block)})
	m = tm.(*model)
	if got, want := m.input.LineCount(), len(lines); got != want {
		t.Fatalf("paste should land all lines: LineCount=%d want %d", got, want)
	}

	tm, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	m = tm.(*model)
	tm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("typed after")})
	m = tm.(*model)
	if got, want := m.input.LineCount(), len(lines)+1; got != want {
		t.Fatalf("ctrl+j after a large paste was swallowed: LineCount=%d want %d\nvalue tail: %q",
			got, want, m.input.Value()[max(0, len(m.input.Value())-120):])
	}
	if !strings.Contains(m.input.Value(), "\ntyped after") {
		t.Errorf("new line should be its own line, got tail %q", m.input.Value()[max(0, len(m.input.Value())-60):])
	}

	if got := m.input.Height(); got != m.input.MaxHeight {
		t.Errorf("box should stay capped at MaxHeight=%d, got %d", m.input.MaxHeight, got)
	}
}

func TestInputScrollsWhenCapped(t *testing.T) {
	m := newGrowModel()
	for i := range m.input.MaxHeight + 5 {
		if i > 0 {
			tm, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
			m = tm.(*model)
		}
		tm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(fmt.Sprintf("row%d", i))})
		m = tm.(*model)
	}
	if got := m.input.Height(); got != m.input.MaxHeight {
		t.Fatalf("should cap at MaxHeight=%d, got %d", m.input.MaxHeight, got)
	}

	if got, want := m.input.LineCount(), m.input.MaxHeight+5; got != want {
		t.Fatalf("content should grow past the visual cap: LineCount=%d want %d\nvalue=%q",
			got, want, m.input.Value())
	}
}
