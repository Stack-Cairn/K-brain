package tui

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// fakeClipboard records what reaches each route.
type fakeClipboard struct {
	nativeErr error
	native    []string
	term      bytes.Buffer
}

func (f *fakeClipboard) clip(env clipboardEnv) clipboard {
	return clipboard{env: env, term: &f.term, native: func(s string) error {
		f.native = append(f.native, s)
		return f.nativeErr
	}}
}

func decodeOSC52(t *testing.T, seq string) string {
	t.Helper()
	body, ok := strings.CutPrefix(seq, "\x1b]52;c;")
	if !ok || !strings.HasSuffix(body, "\a") {
		t.Fatalf("not an OSC 52 sequence: %q", seq)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSuffix(body, "\a"))
	if err != nil {
		t.Fatalf("bad base64 in %q: %v", seq, err)
	}
	return string(raw)
}

func TestClipboardPrefersNativeLocally(t *testing.T) {
	var f fakeClipboard
	route, err := f.clip(clipboardEnv{}).copyText("你好 world")
	if err != nil || route != copyNative {
		t.Fatalf("route=%v err=%v, want native", route, err)
	}
	if len(f.native) != 1 || f.native[0] != "你好 world" || f.term.Len() != 0 {
		t.Fatalf("native=%q term=%q", f.native, f.term.String())
	}
}

func TestClipboardFallsBackToOSC52(t *testing.T) {
	f := fakeClipboard{nativeErr: errors.New("no pbcopy")}
	route, err := f.clip(clipboardEnv{}).copyText("line 1\nline 2")
	if err != nil || route != copyOSC52 {
		t.Fatalf("route=%v err=%v, want OSC 52", route, err)
	}
	if got := decodeOSC52(t, f.term.String()); got != "line 1\nline 2" {
		t.Fatalf("OSC 52 carried %q", got)
	}
}

func TestClipboardOverSSHSkipsNative(t *testing.T) {
	var f fakeClipboard
	route, err := f.clip(clipboardEnv{remote: true}).copyText("remote")
	if err != nil || route != copyOSC52 {
		t.Fatalf("route=%v err=%v, want OSC 52", route, err)
	}
	if len(f.native) != 0 {
		t.Fatalf("SSH copy must not use the remote host's clipboard, got %q", f.native)
	}
	if got := decodeOSC52(t, f.term.String()); got != "remote" {
		t.Fatalf("OSC 52 carried %q", got)
	}
}

func TestClipboardWrapsOSC52ForTmux(t *testing.T) {
	f := fakeClipboard{}
	if _, err := f.clip(clipboardEnv{remote: true, tmux: true}).copyText("x"); err != nil {
		t.Fatal(err)
	}
	want := "\x1bPtmux;\x1b\x1b]52;c;eA==\a\x1b\\"
	if f.term.String() != want {
		t.Fatalf("tmux sequence = %q, want %q", f.term.String(), want)
	}
}

func TestClipboardFailures(t *testing.T) {
	f := fakeClipboard{nativeErr: errors.New("no pbcopy")}
	if _, err := f.clip(clipboardEnv{noOSC52: true}).copyText("x"); err == nil || !strings.Contains(err.Error(), "no pbcopy") {
		t.Fatalf("OSC 52 off and native failing must report the native error, got %v", err)
	}
	if f.term.Len() != 0 {
		t.Fatalf("kill switch still wrote %q", f.term.String())
	}
	if _, err := f.clip(clipboardEnv{remote: true}).copyText(strings.Repeat("a", osc52MaxBytes+1)); !errors.Is(err, errOSC52TooLarge) {
		t.Fatalf("oversized OSC 52 copy: %v", err)
	}
	if _, err := f.clip(clipboardEnv{}).copyText(""); err == nil {
		t.Fatal("empty copy must fail")
	}
}

func csiMsg(seq string) fmt.Stringer {
	parts := make([]string, len(seq))
	for i := range len(seq) {
		parts[i] = fmt.Sprint(seq[i])
	}
	return stringMsg("?CSI[" + strings.Join(parts, " ") + "]?")
}

type stringMsg string

func (s stringMsg) String() string { return string(s) }

func TestIsSuperC(t *testing.T) {
	for seq, want := range map[string]bool{
		"99;9u":  true,  // Cmd+C
		"67;9u":  true,  // Cmd+Shift+C reported as C
		"99;10u": true,  // Cmd+Shift+C
		"99;5u":  false, // Ctrl+C
		"99;13u": false, // Cmd+Ctrl+C
		"99u":    false,
		"118;9u": false, // Cmd+V
	} {
		if got := isSuperC(csiMsg(seq).String()); got != want {
			t.Errorf("isSuperC(%q) = %v, want %v", seq, got, want)
		}
	}
}

// useFakeClipboard routes copies to f for the rest of the test.
func useFakeClipboard(t *testing.T, f *fakeClipboard) {
	prev := clipboardFor
	clipboardFor = func() clipboard { return f.clip(clipboardEnv{}) }
	t.Cleanup(func() { clipboardFor = prev })
}

// dragSecond drags over "second" in the second block and returns the
// release command.
func dragSecond(t *testing.T, m *model) tea.Cmd {
	t.Helper()
	y := blockRowY(m, m.blocks[1].y0)
	m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 0, Y: y})
	m.Update(tea.MouseMsg{Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft, X: 6, Y: y})
	_, cmd := m.Update(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: 6, Y: y})
	if m.sel == nil || !m.sel.done {
		t.Fatal("release must keep a done selection")
	}
	return cmd
}

func TestDragReleaseCopiesSelection(t *testing.T) {
	var f fakeClipboard
	useFakeClipboard(t, &f)
	m := selTestModel()
	cmd := dragSecond(t, m)
	if cmd == nil {
		t.Fatal("release after a drag must copy")
	}
	msg := cmd()
	if len(f.native) != 1 || f.native[0] != "second" {
		t.Fatalf("copied %q, want [second]", f.native)
	}
	tm, expire := m.Update(msg)
	m = tm.(*model)
	if !strings.Contains(ansi.Strip(m.View()), "Copied 6 characters") {
		t.Fatalf("footer must confirm the copy:\n%s", ansi.Strip(m.View()))
	}
	if expire == nil {
		t.Fatal("copy notice must expire")
	}
	m.Update(noticeExpiredMsg(m.noticeGeneration))
	if strings.Contains(ansi.Strip(m.View()), "Copied") {
		t.Fatal("copy notice did not expire")
	}
}

func TestStaleNoticeTimerKeepsNewerNotice(t *testing.T) {
	m := selTestModel()
	m.showNotice("first")
	old := m.noticeGeneration
	m.showNotice("second")
	m.Update(noticeExpiredMsg(old))
	if m.transientNotice != "second" {
		t.Fatalf("older timer cleared the newer notice: %q", m.transientNotice)
	}
}

func TestCopyFailureIsReported(t *testing.T) {
	f := fakeClipboard{nativeErr: errors.New("no pbcopy")}
	prev := clipboardFor
	clipboardFor = func() clipboard { return f.clip(clipboardEnv{noOSC52: true}) }
	t.Cleanup(func() { clipboardFor = prev })
	m := selTestModel()
	m.Update(dragSecond(t, m)())
	if !strings.Contains(m.transientNotice, "Copy failed") || !strings.Contains(m.transientNotice, "no pbcopy") {
		t.Fatalf("notice = %q", m.transientNotice)
	}
}

func TestOSC52CopyNotice(t *testing.T) {
	var f fakeClipboard
	prev := clipboardFor
	clipboardFor = func() clipboard { return f.clip(clipboardEnv{remote: true}) }
	t.Cleanup(func() { clipboardFor = prev })
	m := selTestModel()
	m.Update(dragSecond(t, m)())
	if m.transientNotice != "Sent 6 characters to the terminal clipboard" {
		t.Fatalf("notice = %q", m.transientNotice)
	}
}

func TestCtrlCWithSelectionCopiesInsteadOfArmingQuit(t *testing.T) {
	var f fakeClipboard
	useFakeClipboard(t, &f)
	m := selTestModel()
	dragSecond(t, m)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if m.quit1 {
		t.Fatal("Ctrl+C on a selection must not arm quit")
	}
	if cmd == nil {
		t.Fatal("Ctrl+C on a selection must copy")
	}
	cmd()
	if len(f.native) != 1 || f.native[0] != "second" {
		t.Fatalf("copied %q", f.native)
	}
	if m.sel != nil {
		t.Fatal("Ctrl+C must clear the selection")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if !m.quit1 {
		t.Fatal("Ctrl+C without a selection must arm quit")
	}
}

func TestSuperCWithSelectionCopies(t *testing.T) {
	var f fakeClipboard
	useFakeClipboard(t, &f)
	m := selTestModel()
	dragSecond(t, m)
	_, cmd := m.Update(csiMsg("\x1b[99;9u"[2:]))
	if cmd == nil {
		t.Fatal("Cmd+C on a selection must copy")
	}
	cmd()
	if len(f.native) != 1 || f.native[0] != "second" {
		t.Fatalf("copied %q", f.native)
	}
	if m.sel == nil {
		t.Fatal("Cmd+C keeps the highlight")
	}
}

func TestClickDoesNotCopy(t *testing.T) {
	var f fakeClipboard
	useFakeClipboard(t, &f)
	m := selTestModel()
	y := blockRowY(m, m.blocks[1].y0)
	m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 2, Y: y})
	if _, cmd := m.Update(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: 2, Y: y}); cmd != nil {
		if _, ok := cmd().(copyDoneMsg); ok {
			t.Fatal("a plain click must not copy")
		}
	}
	if len(f.native) != 0 {
		t.Fatalf("click copied %q", f.native)
	}
}
