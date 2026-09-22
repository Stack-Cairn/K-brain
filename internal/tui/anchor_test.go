package tui

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// assertTopAnchored checks the layout invariant: the frame starts at terminal
// row 0, fills exactly the terminal height, ends with the status row, and
// everything past the status row is blank padding. Returns the screen row of
// the input so callers can watch the composer drift.
func assertTopAnchored(t *testing.T, m *model) int {
	t.Helper()
	rendered := m.View()
	if got := lipgloss.Height(rendered); got != m.height {
		t.Fatalf("rendered %d rows, want terminal height %d", got, m.height)
	}
	if m.viewTop != 0 {
		t.Fatalf("frame must start at terminal row 0, got viewTop=%d", m.viewTop)
	}
	lines := strings.Split(ansi.Strip(rendered), "\n")
	statusRow := m.viewH - 1
	if statusRow < 0 || statusRow >= len(lines) {
		t.Fatalf("status row %d outside the %d rendered rows", statusRow, len(lines))
	}
	if got, want := lines[statusRow], ansi.Strip(m.statusView()); got != want {
		t.Fatalf("frame row %d = %q, want status %q", statusRow, got, want)
	}
	for i := statusRow + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != "" {
			t.Fatalf("row %d below the frame should be blank, got %q", i, lines[i])
		}
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

func TestFrameAnchoredToTerminalTop(t *testing.T) {
	for _, height := range []int{9, 24, 30, 50} {
		t.Run(strconv.Itoa(height), func(t *testing.T) {
			m := compactCmdModel()
			m.Update(mkWinSize(80, height))
			m.input.SetValue("ANCHOR-INPUT")
			m.layout()
			start := assertTopAnchored(t, m)

			// A short transcript pushes the composer down, never up.
			m.appendAssistantBlock("Short reply.")
			m.layout()
			grown := assertTopAnchored(t, m)
			if grown < start {
				t.Fatalf("a top-anchored frame must grow downwards: %d -> %d", start, grown)
			}

			// Once the transcript overflows the terminal the composer pins to the
			// bottom and must not move again, including while streaming.
			m.appendAssistantBlock(strings.Repeat("Long reply.\n", 80))
			m.layout()
			full := assertTopAnchored(t, m)
			if m.viewH != m.height {
				t.Fatalf("an overflowing transcript should fill the terminal, viewH=%d height=%d", m.viewH, m.height)
			}

			m.busy = true
			m.current = strings.Repeat("Streaming text.\n", 50)
			m.layout()
			if got := assertTopAnchored(t, m); got != full {
				t.Fatalf("streaming moved the pinned input: %d -> %d", full, got)
			}
			m.busy, m.current = false, ""
			m.layout()
			if got := assertTopAnchored(t, m); got != full {
				t.Fatalf("finishing the turn moved the pinned input: %d -> %d", full, got)
			}
		})
	}
}

func TestTopAnchoredInputResizeAndMultiline(t *testing.T) {
	m := compactCmdModel()
	for _, size := range [][2]int{{80, 40}, {60, 24}, {100, 50}, {80, 9}, {80, 30}} {
		m.Update(mkWinSize(size[0], size[1]))
		m.input.SetValue("ANCHOR-INPUT")
		m.layout()
		row := assertTopAnchored(t, m)

		m.input.SetValue("first\nsecond\nANCHOR-INPUT")
		m.layout()
		if got := assertTopAnchored(t, m); got < row {
			t.Fatalf("multiline input must grow downwards: marker row %d -> %d", row, got)
		}
		m.input.SetValue("ANCHOR-INPUT")
		m.layout()
		if got := assertTopAnchored(t, m); got != row {
			t.Fatalf("shrinking the input did not restore the marker row: %d -> %d", row, got)
		}
	}
}

// composerScreenRow finds the input box's row in the rendered frame.
func composerScreenRow(t *testing.T, m *model) int {
	t.Helper()
	m.layout()
	for i, line := range strings.Split(ansi.Strip(m.View()), "\n") {
		if strings.Contains(line, "Ask k-brain") || strings.Contains(line, "busy — type to queue") {
			return i
		}
	}
	t.Fatal("composer not rendered")
	return -1
}

// Without the bottom pad, a scroll offset left over from a longer transcript
// would keep the top of the new, shorter one off screen with nothing to pull it
// back — viewport.SetContent only clamps against the line count.
func TestScrollOffsetClampedWhenTranscriptShrinks(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(80, 24))
	for i := range 20 {
		m.append(fmt.Sprintf("line-%02d", i))
	}
	m.layout()
	m.View()
	m.follow = false
	m.vp.SetYOffset(11)
	m.View()

	// Still more lines than the offset, so SetContent's own clamp cannot fire.
	m.blocks = m.blocks[:8]
	m.refreshVP()
	m.layout()

	if m.vp.YOffset != 0 {
		t.Fatalf("a transcript that no longer overflows must reset to the top, got YOffset=%d", m.vp.YOffset)
	}
	if !m.follow {
		t.Error("a transcript that cannot scroll must go back to following")
	}
	if got := strings.TrimRight(strings.Split(ansi.Strip(m.View()), "\n")[0], " "); got != "line-00" {
		t.Fatalf("first transcript row = %q, want line-00", got)
	}
}

// Trimming blank rows off the viewport would eat real separator rows, so the
// frame height — and with it the composer and the effort chip's click target —
// changed by a row on every scroll notch.
func TestComposerStaysPutWhileScrolling(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(80, 24))
	for i := range 40 {
		m.appendAssistantBlock(fmt.Sprintf("reply %02d", i))
	}
	m.layout()
	m.View()
	m.follow = false

	m.vp.SetYOffset(10)
	want, wantEffort := composerScreenRow(t, m), m.effortY
	for off := 11; off <= 30; off++ {
		m.vp.SetYOffset(off)
		if got := composerScreenRow(t, m); got != want {
			t.Fatalf("scrolling to offset %d moved the composer: %d -> %d", off, want, got)
		}
		if m.effortY != wantEffort {
			t.Fatalf("scrolling to offset %d moved the effort chip: %d -> %d", off, wantEffort, m.effortY)
		}
	}
}

// A markdown fence opening, or the live area emptying on a flush, briefly
// shrinks the frame. Top-anchoring would pass that straight to the composer as
// upward jitter, so a turn holds its high-water mark.
func TestComposerNeverRisesMidTurn(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(80, 30))
	m.busy = true

	rows := []int{composerScreenRow(t, m)}
	for _, chunk := range []string{"text before\n", "```go\n", "func main() {\n", "\tx := 1\n", "}\n", "```\n", "after\n"} {
		for _, half := range []string{chunk[:len(chunk)/2], chunk[len(chunk)/2:]} {
			m.Update(textMsg(half))
			rows = append(rows, composerScreenRow(t, m))
		}
	}
	for i := 1; i < len(rows); i++ {
		if rows[i] < rows[i-1] {
			t.Fatalf("composer rose mid-turn at step %d: %v", i, rows)
		}
	}

	// The floor is released when the turn ends.
	m.busy, m.current, m.curThink = false, "", ""
	m.layout()
	m.View()
	if m.turnFloorH != 0 {
		t.Errorf("turn floor should be released when idle, got %d", m.turnFloorH)
	}
}
