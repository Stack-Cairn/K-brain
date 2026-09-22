package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func TestInlineRendering(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(80, 30))
	m.appendAssistant("hello **world**")
	v := m.View()
	if strings.Contains(v, "\x1b[?1049h") || strings.Contains(v, "\x1b[?47h") {
		t.Fatal("view must not enter the alternate screen")
	}
	for _, want := range []string{"Ask k-brain anything", "hello", "world"} {
		if !strings.Contains(stripAll(v), want) {
			t.Errorf("inline view missing %q", want)
		}
	}

	if !m.mouseOn {
		t.Fatal("mouse capture must default on for wheel scroll")
	}
}

func TestInlineViewReturnsToTopAfterTemporaryGrowth(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(80, 30))
	m.View()
	base := m.viewH

	m.busy = true
	m.layout()
	grown := m.View()
	if m.viewH != m.height {
		t.Fatalf("the frame must fill the terminal: got %d, want %d", m.viewH, m.height)
	}
	if !strings.Contains(stripAll(grown), "thinking") {
		t.Fatal("the busy row must stay visible")
	}
	if m.viewTop != 0 {
		t.Fatalf("a growing frame must stay anchored at row 0, got %d", m.viewTop)
	}

	m.busy = false
	m.layout()
	shrunk := m.View()
	if m.viewH != base {
		t.Fatalf("shrunk frame must return to its original height: got %d, want %d", m.viewH, base)
	}
	if m.viewTop != 0 {
		t.Fatalf("a shrinking frame must stay anchored at row 0, got %d", m.viewTop)
	}
	if got, want := lipgloss.Height(shrunk), lipgloss.Height(grown); got != want {
		t.Fatalf("both renders must fill the terminal: got %d, want %d", got, want)
	}
}

func TestInlineFrameHeightIsCappedByTerminal(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(80, 9))
	m.View()

	m.busy = true
	m.layout()
	busy := m.View()
	if got := lipgloss.Height(busy); got != m.height {
		t.Fatalf("busy frame renders %d rows on a %d-row terminal", got, m.height)
	}

	m.busy = false
	m.layout()
	shrunk := m.View()

	// The transcript is top-anchored and the composer bottom-anchored, so an
	// empty session legitimately opens with blank rows between them. What must
	// hold is that the frame fills the terminal exactly and the composer's rule
	// and input stay adjacent — the frame must never be split by the padding.
	if m.viewTop != 0 {
		t.Fatalf("viewTop must be 0 under top anchoring, got %d", m.viewTop)
	}
	if m.viewH > m.height {
		t.Fatalf("frame height %d exceeds the terminal's %d", m.viewH, m.height)
	}
	if got := lipgloss.Height(shrunk); got != m.height {
		t.Fatalf("shrunk frame renders %d rows on a %d-row terminal", got, m.height)
	}
	assertComposerWhole(t, m)
}

// assertComposerWhole checks the input box still has its rule directly above it.
// Padding inserted at inputBodyOff rather than composerTop lands inside the
// frame and strands the top rule up with the transcript.
func assertComposerWhole(t *testing.T, m *model) {
	t.Helper()
	lines := strings.Split(ansi.Strip(m.View()), "\n")
	input := -1
	for i, l := range lines {
		if strings.Contains(l, "Ask k-brain") || strings.Contains(l, "busy — type to queue") {
			input = i
			break
		}
	}
	if input < 1 {
		t.Fatalf("composer not found in:\n%s", strings.Join(lines, "\n"))
	}
	if above := strings.TrimSpace(lines[input-1]); strings.Trim(above, "─") != "" || above == "" {
		t.Fatalf("row above the input should be the composer's rule, got %q", lines[input-1])
	}
	if input+1 < len(lines) {
		if below := strings.TrimSpace(lines[input+1]); strings.Trim(below, "─") != "" || below == "" {
			t.Fatalf("row below the input should be the composer's rule, got %q", lines[input+1])
		}
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
