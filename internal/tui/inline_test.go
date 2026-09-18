package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestInlineRendering(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(80, 30))
	m.appendAssistant("hello **world**")
	v := m.View()
	if strings.Contains(v, "\x1b[?1049h") || strings.Contains(v, "\x1b[?47h") {
		t.Fatal("view must not enter the alternate screen")
	}
	for _, want := range []string{"k-brain ·", "hello", "world"} {
		if !strings.Contains(stripAll(v), want) {
			t.Errorf("inline view missing %q", want)
		}
	}

	if !m.mouseOn {
		t.Fatal("mouse capture must default on for wheel scroll")
	}
}

func TestInlineViewReturnsToBottomAfterTemporaryGrowth(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(80, 30))
	m.View()
	bottom := m.viewTop

	m.busy = true
	m.layout()
	grown := m.View()
	if m.viewTop >= bottom {
		t.Fatalf("test setup: growing view must move up: %d -> %d", bottom, m.viewTop)
	}

	m.busy = false
	m.layout()
	shrunk := m.View()
	if m.viewTop != bottom {
		t.Fatalf("shrunk view must return to bottom: got top %d, want %d", m.viewTop, bottom)
	}
	if got, want := lipgloss.Height(shrunk), lipgloss.Height(grown); got != want {
		t.Fatalf("shrunk render must retain the physical frame height: got %d, want %d", got, want)
	}
}

func TestInlineFrameHeightIsCappedByTerminal(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(80, 9))
	m.View()

	m.busy = true
	m.layout()
	m.View()

	m.busy = false
	m.layout()
	shrunk := m.View()

	lead := len(shrunk) - len(strings.TrimLeft(shrunk, "\n"))
	dropped := max(lipgloss.Height(shrunk)-m.height, 0)
	physicalContentTop := max(lead-dropped, 0)
	if m.viewTop != physicalContentTop {
		t.Fatalf("viewTop must account for renderer clipping: got %d, want %d", m.viewTop, physicalContentTop)
	}
	if got, want := m.viewTop+m.viewH, m.height; got != want {
		t.Fatalf("shrunk content must remain bottom-anchored: got bottom %d, want %d", got, want)
	}
}

func stripAll(s string) string {
	out := strings.Builder{}
	i := 0
	for i < len(s) {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' && s[i] != 'h' && s[i] != 'l' {
				i++
			}
			i++
			continue
		}
		out.WriteByte(s[i])
		i++
	}
	return out.String()
}
