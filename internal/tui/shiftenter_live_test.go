package tui

import (
	"fmt"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type unknownCSI []byte

func (u unknownCSI) String() string { return fmt.Sprintf("?CSI%+v?", []byte(u)[2:]) }

func TestShiftEnterViaUnknownCSIInsertsNewline(t *testing.T) {
	m := compactCmdModel()
	m.input.SetValue("aaa")

	for _, seq := range []string{
		"\x1b[13;2u",
		"\x1b[27;2;13~",
		"\x1b[57441u",
	} {
		m.input.SetValue("aaa")
		m.input.CursorEnd()
		tm, _ := m.Update(unknownCSI(seq))
		m = tm.(*model)
		if got := m.input.Value(); got != "aaa\n" {
			t.Errorf("Update(%q): want input %q, got %q", seq, "aaa\n", got)
		}
	}

	m.input.SetValue("aaa")
	tm, _ := m.Update(unknownCSI("\x1b[1;2A"))
	m = tm.(*model)
	if got := m.input.Value(); got != "aaa" {
		t.Errorf("shift+up must not insert a newline, got %q", got)
	}
}

func TestCSIUKeysDecodeToLegacyKeys(t *testing.T) {
	cases := map[string]tea.KeyMsg{
		"\x1b[97;5u":    {Type: tea.KeyCtrlA},
		"\x1b[101;5u":   {Type: tea.KeyCtrlE},
		"\x1b[99;5u":    {Type: tea.KeyCtrlC},
		"\x1b[27u":      {Type: tea.KeyEsc},
		"\x1b[120;3u":   {Type: tea.KeyRunes, Runes: []rune{'x'}, Alt: true},
		"\x1b[97:65;2u": {Type: tea.KeyRunes, Runes: []rune{'A'}},
		"\x1b[99;7u":    {Type: tea.KeyCtrlC, Alt: true},
		"\x1b[32;2u":    {Type: tea.KeySpace},
	}
	for seq, want := range cases {
		got, ok := csiUKey(unknownCSI(seq).String())
		if !ok || got.String() != want.String() || got.Alt != want.Alt {
			t.Errorf("%q: got %v (ok=%v), want %v", seq, got, ok, want)
		}
	}
	for _, seq := range []string{"\x1b[1;2A", "\x1b[57441u", "\x1b[200~"} {
		if _, ok := csiUKey(unknownCSI(seq).String()); ok {
			t.Errorf("%q must not decode as a CSI-u ASCII key", seq)
		}
	}

	m := compactCmdModel()
	m.input.SetValue("abc")
	m.input.CursorEnd()
	tm, _ := m.Update(unknownCSI("\x1b[97;5u"))
	m = tm.(*model)
	m.input.InsertString("X")
	tm, _ = m.Update(unknownCSI("\x1b[101;5u"))
	m = tm.(*model)
	m.input.InsertString("Y")
	if got := m.input.Value(); got != "XabcY" {
		t.Errorf("ctrl+a/ctrl+e via CSI u: want %q, got %q", "XabcY", got)
	}
}

func TestAltBOnBlankLineDoesNotHang(t *testing.T) {
	for _, k := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune{'b'}, Alt: true},
		{Type: tea.KeyLeft, Alt: true},
	} {
		m := compactCmdModel()
		m.input.SetValue("abc\n\nxyz")
		m.Update(mkWinSize(80, 24))
		m.input.CursorUp()
		done := make(chan *model, 1)
		go func() {
			tm, _ := m.Update(k)
			done <- tm.(*model)
		}()
		select {
		case mm := <-done:
			if mm.input.Line() != 0 {
				t.Errorf("%s: want cursor on row 0, got row %d", k, mm.input.Line())
			}
			mm.input.InsertString("!")
			if got := mm.input.Value(); got != "abc!\n\nxyz" {
				t.Errorf("%s: want cursor at end of previous line, got %q", k, got)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("%s on a blank line hung (bubbles wordLeft infinite loop)", k)
		}
	}

	m := compactCmdModel()
	m.input.SetValue("foo bar")
	m.input.CursorEnd()
	tm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}, Alt: true})
	m = tm.(*model)
	m.input.InsertString("!")
	if got := m.input.Value(); got != "foo !bar" {
		t.Errorf("alt+b word motion broken: %q", got)
	}
}
