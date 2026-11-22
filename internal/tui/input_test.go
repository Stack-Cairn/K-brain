package tui

import (
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func keyRunes(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func TestIsShiftEnterSeq(t *testing.T) {
	for in, want := range map[string]bool{

		"unknown csi sequence: 0x1b, '[', '1', '3', ';', '2', 'u'":                     true,
		"unknown csi sequence: 0x1b, '[', '2', '7', ';', '2', ';', '1', '3', '~'":      true,
		"unknown csi sequence: 0x1b, '[', 'five', 'seven', 'four', 'four', 'one', 'u'": true,
		"unknown csi sequence: 0x1b, '[', '1', ';', '2', 'A'":                          false,

		"?CSI[49 51 59 50 117]?":          true,
		"?CSI[50 55 59 50 59 49 51 126]?": true,
		"?CSI[53 55 52 52 49 117]?":       true,
		"?CSI[49 59 50 65]?":              false,
		"a":                               false,
		"enter":                           false,
	} {
		if got := isShiftEnterSeq(keyRunes(in)); got != want {
			t.Errorf("isShiftEnterSeq(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestCtrlEGoesToLineEnd(t *testing.T) {
	m := compactCmdModel()
	m.input.SetValue("hello world")
	m.input.CursorStart()

	tm, _ := m.key(tea.KeyMsg{Type: tea.KeyCtrlE})
	m = tm.(*model)
	if got := m.input.LineInfo().CharOffset; got != len("hello world") {
		t.Fatalf("ctrl+e should go to line end, char offset = %d", got)
	}

	// With a tool block to expand Ctrl+E still only moves the cursor; Alt+O
	// is the expand key.
	m.blocks = append(m.blocks, block{kind: blockTool, text: "ran a thing"})
	m.input.CursorStart()
	tm, _ = m.key(tea.KeyMsg{Type: tea.KeyCtrlE})
	m = tm.(*model)
	if m.blocks[len(m.blocks)-1].expanded {
		t.Fatal("ctrl+e must not expand a tool block")
	}
	if got := m.input.LineInfo().CharOffset; got != len("hello world") {
		t.Fatalf("ctrl+e should go to line end with a tool block too, offset = %d", got)
	}
	m.input.CursorStart()
	tm, _ = m.key(altKey("o"))
	m = tm.(*model)
	if !m.blocks[len(m.blocks)-1].expanded {
		t.Fatal("alt+o with a tool block should expand it")
	}
	if got := m.input.Value(); got != "hello world" || m.input.LineInfo().CharOffset != 0 {
		t.Fatalf("alt+o must leave the draft alone: %q at %d", got, m.input.LineInfo().CharOffset)
	}
}

func TestCtrlAGoesToLineStart(t *testing.T) {
	m := compactCmdModel()
	m.input.SetValue("hello world")
	m.input.CursorEnd()
	tm, _ := m.key(tea.KeyMsg{Type: tea.KeyCtrlA})
	m = tm.(*model)
	if got := m.input.LineInfo().CharOffset; got != 0 {
		t.Fatalf("ctrl+a should go to line start, char offset = %d", got)
	}
}

func TestKeyboardEnhancementEscapes(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TERM", "xterm-256color")
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	enableKeyboardEnhancement(w)
	disableKeyboardEnhancement(w)
	w.Close()
	buf := make([]byte, 64)
	_ = r.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _ := r.Read(buf)
	got := string(buf[:n])
	if !strings.Contains(got, "\x1b[>1u") {
		t.Errorf("must push kitty disambiguate flag \\x1b[>1u, got %q", got)
	}
	if !strings.Contains(got, "\x1b[<u") {
		t.Errorf("must pop the keyboard stack \\x1b[<u, got %q", got)
	}
	if strings.Contains(got, "\x1b[>4;") {
		t.Errorf("modifyOtherKeys is tmux-only, got %q", got)
	}
}

func TestKeyboardEnhancementTmuxRequestsModifyOtherKeys1(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-1/default,1,0")
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	enableKeyboardEnhancement(w)
	disableKeyboardEnhancement(w)
	w.Close()
	buf := make([]byte, 128)
	_ = r.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _ := r.Read(buf)
	got := string(buf[:n])
	for _, want := range []string{"\x1bPtmux;\x1b\x1b[>1u\x1b\\", "\x1b[>4;1m", "\x1b[>4;0m"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "\x1b[>4;2m") {
		t.Errorf("mode 2 breaks ctrl+letter: %q", got)
	}
}

func TestTmuxPassthrough(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-1000/default,1,0")
	got := tmuxPassthrough("\x1b[>1u")
	want := "\x1bPtmux;\x1b\x1b[>1u\x1b\\"
	if got != want {
		t.Errorf("tmux passthrough = %q, want %q", got, want)
	}
}

func TestTmuxPassthroughOutsideTmux(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TERM", "xterm-256color")
	if got := tmuxPassthrough("\x1b[>1u"); got != "\x1b[>1u" {
		t.Errorf("outside tmux the sequence passes through unchanged, got %q", got)
	}
}

func TestProcHasAncestor(t *testing.T) {
	self, err := os.ReadFile("/proc/self/comm")
	if err != nil {
		t.Skip("/proc unavailable")
	}
	name := strings.TrimSpace(string(self))
	if !procHasAncestor(os.Getpid(), name) {
		t.Errorf("own process %q should be found walking its own chain", name)
	}
	if procHasAncestor(os.Getpid(), "no-such-proc-xyzzy") {
		t.Error("a nonexistent process name must not match")
	}
	if procHasAncestor(1, name) {
		t.Error("walking from pid 1 must not find the test binary")
	}
}
