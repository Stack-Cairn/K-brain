package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func assertBottomAnchored(t *testing.T, m *model) int {
	t.Helper()
	rendered := m.View()
	if got := lipgloss.Height(rendered); got != m.height {
		t.Fatalf("rendered %d rows, want terminal height %d", got, m.height)
	}
	lines := strings.Split(ansi.Strip(rendered), "\n")
	if got, want := lines[len(lines)-1], ansi.Strip(m.statusView()); got != want {
		t.Fatalf("last terminal row = %q, want status %q", got, want)
	}
	row := -1
	for i, line := range lines {
		if strings.Contains(line, "ANCHOR-INPUT") {
			row = i
			break
		}
	}
	if row < 0 {
		t.Fatal("input is not visible")
	}
	if m.inputTop < 0 || m.inputTop >= m.height {
		t.Fatalf("input mouse origin outside terminal: %d", m.inputTop)
	}
	if again := m.View(); again != rendered {
		t.Fatal("rendering an unchanged frame moved its content")
	}
	return row
}

func TestInputAnchoredToTerminalBottom(t *testing.T) {
	for _, height := range []int{9, 24, 30, 50} {
		t.Run(fmt.Sprint(height), func(t *testing.T) {
			m := compactCmdModel()
			m.Update(mkWinSize(80, height))
			m.input.SetValue("ANCHOR-INPUT")
			m.layout()
			row := assertBottomAnchored(t, m)
			for _, text := range []string{"Short reply.", strings.Repeat("Long reply.\n", 80)} {
				m.appendAssistantBlock(text)
				m.layout()
				if got := assertBottomAnchored(t, m); got != row {
					t.Fatalf("transcript growth moved input: %d -> %d", row, got)
				}
			}
			m.busy = true
			m.current = strings.Repeat("Streaming text.\n", 50)
			m.layout()
			if got := assertBottomAnchored(t, m); got != row {
				t.Fatalf("streaming moved input: %d -> %d", row, got)
			}
			m.busy, m.current = false, ""
			m.layout()
			if got := assertBottomAnchored(t, m); got != row {
				t.Fatalf("finishing turn moved input: %d -> %d", row, got)
			}
		})
	}
}

func TestBottomAnchoredInputResizeAndMultiline(t *testing.T) {
	m := compactCmdModel()
	for _, size := range [][2]int{{80, 40}, {60, 24}, {100, 50}, {80, 9}, {80, 30}} {
		m.Update(mkWinSize(size[0], size[1]))
		m.input.SetValue("ANCHOR-INPUT")
		m.layout()
		row := assertBottomAnchored(t, m)
		m.input.SetValue("first\nsecond\nANCHOR-INPUT")
		m.layout()
		if got := assertBottomAnchored(t, m); got != row {
			t.Fatalf("multiline input must grow upwards: last row %d -> %d", row, got)
		}
		m.input.SetValue("ANCHOR-INPUT")
		m.layout()
		if got := assertBottomAnchored(t, m); got != row {
			t.Fatalf("shrinking input moved bottom row: %d -> %d", row, got)
		}
	}
}
