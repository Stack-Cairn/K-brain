package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// In transcript focus Ctrl+U / Ctrl+D scroll half a page; in the composer
// Ctrl+U stays the kill to line start.
func TestTranscriptFocusHalfPage(t *testing.T) {
	m := focusModel(80, 16)
	for range 3 {
		m.appendAssistantBlock(strings.Repeat("filler line\n", 10) + "end")
	}
	m.refreshVP()
	m.View()
	m = keys(t, m, "alt+up")
	bottom := m.vp.YOffset
	half := m.vp.Height / 2
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	if got := m.vp.YOffset; got != bottom-half {
		t.Fatalf("Ctrl+U should scroll up half a page (%d), offset %d → %d", half, bottom, got)
	}
	if m.follow {
		t.Fatal("scrolling up stops following")
	}
	if b := m.focused(); b == nil || b.y1 < m.vp.YOffset || b.y0 >= m.vp.YOffset+m.vp.Height {
		t.Fatal("focus should stay on a block on screen")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	if got := m.vp.YOffset; got != bottom {
		t.Fatalf("Ctrl+D should scroll back down, offset %d, want %d", got, bottom)
	}
	if m.input.Value() != "" {
		t.Fatalf("the composer must not see focus keys: %q", m.input.Value())
	}

	m = keys(t, m, "esc")
	m.setComposer("keep this", len("keep "))
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	if got := m.input.Value(); got != "this" {
		t.Fatalf("Ctrl+U in the composer cuts to line start, got %q", got)
	}
}

// Scrolled up, End on an empty composer (or Ctrl+End) jumps back to the
// newest output, and the footer names the key.
func TestEndJumpsToNewestOutput(t *testing.T) {
	m := focusModel(100, 16)
	for range 3 {
		m.appendAssistantBlock(strings.Repeat("filler line\n", 10) + "end")
	}
	m.refreshVP()
	m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	if !m.scrolledUp() {
		t.Fatal("PgUp should scroll up")
	}
	if got := m.footerStats(); !strings.Contains(got, "End ↓") || strings.Contains(got, "Ctrl+End") {
		t.Fatalf("footer should name End on an empty composer: %q", got)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	if m.scrolledUp() || !m.follow {
		t.Fatal("End on an empty composer should jump to the newest output")
	}

	// With a draft End is the line end; Ctrl+End jumps and the footer says so.
	m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	m.setComposer("draft text", 0)
	if got := m.footerStats(); !strings.Contains(got, "Ctrl+End ↓") {
		t.Fatalf("footer should name Ctrl+End with a draft: %q", got)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	if !m.scrolledUp() {
		t.Fatal("End with a draft must not scroll")
	}
	if got := m.input.LineInfo().CharOffset; got != len("draft text") {
		t.Fatalf("End with a draft goes to the line end, offset %d", got)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlEnd})
	if m.scrolledUp() {
		t.Fatal("Ctrl+End should jump to the newest output")
	}
	if m.input.Value() != "draft text" {
		t.Fatalf("the draft stays: %q", m.input.Value())
	}
}
