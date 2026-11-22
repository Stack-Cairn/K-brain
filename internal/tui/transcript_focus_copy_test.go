package tui

import (
	"strings"
	"testing"
)

// [ and ] jump between prompts, skipping the blocks between them.
func TestFocusJumpsBetweenTurns(t *testing.T) {
	m := focusModel(80, 40)
	m.appendRaw(blockUser, "And after a second resize?")
	m.appendAssistantBlock("The same: one miss per entry.")
	m.refreshVP()
	m.View()
	m = keys(t, m, "alt+up")
	if b := m.focused(); b == nil || b.text != "The same: one miss per entry." {
		t.Fatalf("Alt+↑ focused %+v", b)
	}
	m = keys(t, m, "[")
	if b := m.focused(); b == nil || b.kind != blockUser || !strings.Contains(b.text, "second resize") {
		t.Fatalf("[ should focus the last prompt, got %+v", b)
	}
	m = keys(t, m, "[")
	if b := m.focused(); b == nil || b.kind != blockUser || !strings.Contains(b.text, "cache miss") {
		t.Fatalf("[ again should focus the first prompt, got %+v", b)
	}
	m = keys(t, m, "[")
	if b := m.focused(); b == nil || !strings.Contains(b.text, "cache miss") {
		t.Fatalf("[ at the first prompt stays put, got %+v", b)
	}
	m = keys(t, m, "]")
	if b := m.focused(); b == nil || !strings.Contains(b.text, "second resize") {
		t.Fatalf("] should focus the next prompt, got %+v", b)
	}
	if m.input.Value() != "" {
		t.Fatalf("focus keys leaked into the composer: %q", m.input.Value())
	}
}

// y copies a reply's markdown source, only the code of a reply that is
// one code block, and a tool result's plain text.
func TestFocusCopiesBlockSource(t *testing.T) {
	var f fakeClipboard
	useFakeClipboard(t, &f)
	m := focusModel(80, 40)
	m.appendAssistantBlock("```go\nfunc key(w int) int { return w }\n```")
	m.refreshVP()
	m.View()
	copyFocus := func() string {
		t.Helper()
		_, cmd := m.Update(keyRunes("y"))
		if cmd == nil {
			t.Fatal("y should copy")
		}
		cmd()
		if len(f.native) == 0 {
			t.Fatal("nothing reached the clipboard")
		}
		return f.native[len(f.native)-1]
	}
	m = keys(t, m, "alt+up")
	if got := copyFocus(); got != "func key(w int) int { return w }" {
		t.Fatalf("a code-block reply copies just its code, got %q", got)
	}
	m = keys(t, m, "k")
	if got := copyFocus(); got != "Keying by width is intended." {
		t.Fatalf("got %q", got)
	}
	m = keys(t, m, "k", "k", "k")
	if b := m.focused(); b == nil || b.kind != blockAssistant {
		t.Fatalf("want the first reply focused, got %+v", b)
	}
	if got := copyFocus(); !strings.HasPrefix(got, "The cache is keyed by **width**.") {
		t.Fatalf("a reply copies its markdown source, got %q", got)
	}
	m = keys(t, m, "j", "j")
	if b := m.focused(); b == nil || b.kind != blockTool {
		t.Fatalf("want the tool output focused, got %+v", b)
	}
	if got := copyFocus(); !strings.HasPrefix(got, "cache.go:10: key := width + 0") {
		t.Fatalf("a tool output copies its text, got %q", got)
	}
}

// r flips a reply between its rendered form and its markdown source.
func TestFocusRawView(t *testing.T) {
	m := focusModel(140, 40)
	m = keys(t, m, "alt+up", "k", "k", "k")
	if b := m.focused(); b == nil || b.kind != blockAssistant {
		t.Fatalf("want the first reply focused, got %+v", b)
	}
	if v := stripView(m.View()); strings.Contains(v, "**width**") {
		t.Fatal("the rendered reply should not show markdown markers")
	}
	m = keys(t, m, "r")
	v := stripView(m.View())
	if !strings.Contains(v, "The cache is keyed by **width**.") {
		t.Fatalf("r should show the markdown source:\n%s", v)
	}
	if !strings.Contains(v, "r raw") {
		t.Fatalf("the focus bar should name r:\n%s", v)
	}
	m = keys(t, m, "r")
	if v := stripView(m.View()); strings.Contains(v, "**width**") {
		t.Fatal("r again should render the reply")
	}
	// A prompt has no raw form; r says so and changes nothing.
	m = keys(t, m, "[", "r")
	if b := m.focused(); b == nil || b.kind != blockUser || b.raw {
		t.Fatalf("r on a prompt should do nothing, got %+v", b)
	}
	if m.transientNotice == "" {
		t.Fatal("r on a prompt should say why nothing happened")
	}
}
