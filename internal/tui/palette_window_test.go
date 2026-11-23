package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// The palette has more items than a common terminal has rows. Its title,
// filter and footer stay on screen and only the list scrolls, keeping the
// selection in view.
func TestPaletteKeepsHeaderAndFooterOnScreen(t *testing.T) {
	for _, size := range [][2]int{{100, 30}, {120, 40}, {80, 16}} {
		m, _ := stClock(t)
		m.Update(mkWinSize(size[0], size[1]))
		m.openPalette()
		check := func(when string) []string {
			t.Helper()
			lines := strings.Split(ansi.Strip(m.View()), "\n")
			if len(lines) > size[1] {
				t.Fatalf("%dx%d %s: %d lines", size[0], size[1], when, len(lines))
			}
			if !strings.Contains(strings.Join(lines[:2], "\n"), "Commands") || !strings.Contains(strings.Join(lines[:5], "\n"), "❯") {
				t.Fatalf("%dx%d %s: title scrolled away:\n%s", size[0], size[1], when, strings.Join(lines, "\n"))
			}
			if !strings.Contains(strings.Join(lines, "\n"), "↑/↓ select") {
				t.Fatalf("%dx%d %s: footer missing", size[0], size[1], when)
			}
			return lines
		}
		check("on open")
		for range len(m.palette.items) - 1 {
			tm, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
			m = tm.(*model)
		}
		lines := check("at the last item")
		last := m.palette.items[len(m.palette.items)-1].title
		if !strings.Contains(strings.Join(lines, "\n"), last) {
			t.Fatalf("%dx%d: selected %q not shown", size[0], size[1], last)
		}
	}
}
