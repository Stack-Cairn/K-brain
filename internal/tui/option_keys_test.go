package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func useOnMac(t *testing.T, mac bool) {
	prev := onMac
	onMac = mac
	t.Cleanup(func() { onMac = prev })
}

// Off by default: Option characters are typed as they are, and on macOS the
// first one in an empty composer adds a single note pointing at /doctor.
func TestOptionCharactersTypeByDefault(t *testing.T) {
	useOnMac(t, true)
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	m := compactCmdModel()
	m.Update(mkWinSize(100, 30))
	n := len(m.blocks)
	m.Update(keyRunes("ø"))
	m.Update(keyRunes("l"))
	if got := m.input.Value(); got != "øl" {
		t.Fatalf("Option characters must stay text by default: %q", got)
	}
	if len(m.blocks) != n+1 || !strings.Contains(m.blocks[n].text, "Option+O") || !strings.Contains(m.blocks[n].text, "/doctor") {
		t.Fatalf("want one note naming Option+O and /doctor, blocks %d → %d", n, len(m.blocks))
	}
	m.input.Reset()
	m.Update(keyRunes("†"))
	if len(m.blocks) != n+1 {
		t.Fatal("the note shows once")
	}
	if m.input.Value() != "†" {
		t.Fatalf("got %q", m.input.Value())
	}
}

// Typing inside a word never adds the note, and off macOS it never shows.
func TestOptionNoteOnlyOnMacEmptyComposer(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	for _, mac := range []bool{true, false} {
		useOnMac(t, mac)
		m := compactCmdModel()
		m.Update(mkWinSize(100, 30))
		n := len(m.blocks)
		if mac {
			m.Update(keyRunes("k"))
		}
		m.Update(keyRunes("ø"))
		if len(m.blocks) != n {
			t.Fatalf("mac=%v: no note expected", mac)
		}
	}
}

// With "Option as Alt" on, the mapped characters act as Alt shortcuts:
// Option+O (ø) toggles the last tool output, as Alt+O does.
func TestOptionAsAltMapsShortcuts(t *testing.T) {
	useOnMac(t, true)
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	m := compactCmdModel()
	m.Update(mkWinSize(100, 30))
	on := true
	m.cfg.OptionAsAlt = &on
	got := m.optionKey(keyRunes("ø"))
	if got.String() != "alt+o" {
		t.Fatalf("ø should read as alt+o, got %q", got.String())
	}
	for _, r := range "b漢éñ" {
		if k := m.optionKey(keyRunes(string(r))); k.Alt {
			t.Errorf("%q must stay a plain key", r)
		}
	}
	if k := m.optionKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ø"), Paste: true}); k.Alt {
		t.Error("a paste is never a shortcut")
	}
	// Alt+B moves a word left in the composer; ∫ does the same now.
	m.setComposer("one two", 7)
	m.Update(keyRunes("∫"))
	if m.input.Value() != "one two" || m.composerSnap().off != 4 {
		t.Fatalf("∫ should move a word left: %q at %d", m.input.Value(), m.composerSnap().off)
	}
}

func TestOptionKeyDoctorLine(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	m := compactCmdModel()
	useOnMac(t, false)
	if m.optionKeyDoctor() != "" {
		t.Fatal("no Option line off macOS")
	}
	useOnMac(t, true)
	if got := m.optionKeyDoctor(); !strings.Contains(got, "Use Option as Meta key") || !strings.Contains(got, "Esc+") {
		t.Fatalf("doctor line: %q", got)
	}
	if !strings.Contains(m.doctorReport(), "option key") {
		t.Fatal("/doctor should carry the Option line on macOS")
	}
}
