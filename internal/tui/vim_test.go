package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// vimModel is a model with vim mode on and the draft val, the cursor at
// rune offset off, in normal mode.
func vimModel(t *testing.T, val string, off int) *model {
	t.Helper()
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	m := compactCmdModel()
	m.Update(mkWinSize(100, 30))
	on := true
	m.cfg.Vim = &on
	m.setComposer(val, off)
	m.composer().vim.normal = true
	return m
}

// vimKeys sends keys: single runes, or "esc" / "enter".
func vimKeys(m *model, ks ...string) {
	for _, k := range ks {
		switch k {
		case "esc":
			m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		case "enter":
			m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		default:
			for _, r := range k {
				m.Update(keyRunes(string(r)))
			}
		}
	}
}

func vimCursor(m *model) int { return m.composerSnap().off }

func TestVimMotions(t *testing.T) {
	const text = "foo.bar baz\n  qux quux"
	for _, tc := range []struct {
		keys string
		from int
		want int
	}{
		{"l", 0, 1},
		{"h", 0, 0},
		{"$", 0, 10},
		{"0", 5, 0},
		{"w", 0, 3},  // foo → .
		{"ww", 0, 4}, // . → bar
		{"www", 0, 8},
		{"wwww", 0, 14}, // across the newline, over the indent
		{"e", 0, 2},
		{"ee", 0, 3},
		{"b", 8, 4},
		{"bb", 8, 3},
		{"j", 1, 13},
		{"jk", 1, 1},
		{"^", 18, 14},
		{"$", 12, 21},
		{"l", 10, 10}, // never past the last character
	} {
		m := vimModel(t, text, tc.from)
		vimKeys(m, tc.keys)
		if got := vimCursor(m); got != tc.want {
			t.Errorf("%q from %d: cursor %d, want %d", tc.keys, tc.from, got, tc.want)
		}
		if m.input.Value() != text {
			t.Errorf("%q changed the draft to %q", tc.keys, m.input.Value())
		}
	}
}

func TestVimEdits(t *testing.T) {
	for _, tc := range []struct {
		val    string
		from   int
		keys   string
		want   string
		cursor int
		insert bool
	}{
		{"hello world", 0, "x", "ello world", 0, false},
		{"hello world", 10, "x", "hello worl", 9, false},
		{"hello world", 0, "dw", "world", 0, false},
		{"hello world", 6, "dw", "hello ", 5, false},
		{"one\ntwo\nthree", 5, "dd", "one\nthree", 4, false},
		{"one\ntwo", 5, "dd", "one", 0, false},
		{"hello world", 6, "D", "hello ", 5, false},
		{"hello world", 0, "cw", " world", 0, true},
		{"one\ntwo\nthree", 5, "cc", "one\n\nthree", 4, true},
		{"hello world", 5, "C", "hello", 5, true},
		{"one\ntwo", 0, "yyp", "one\none\ntwo", 4, false},
		{"one\ntwo", 0, "yyjp", "one\ntwo\none", 8, false},
		{"ab", 0, "xp", "ba", 1, false},
		{"abc", 1, "i", "abc", 1, true},
		{"abc", 1, "a", "abc", 2, true},
		{"  abc", 4, "I", "  abc", 2, true},
		{"abc", 0, "A", "abc", 3, true},
		{"one\ntwo", 1, "o", "one\n\ntwo", 4, true},
		{"one\ntwo", 5, "O", "one\n\ntwo", 4, true},
	} {
		m := vimModel(t, tc.val, tc.from)
		vimKeys(m, tc.keys)
		if got := m.input.Value(); got != tc.want {
			t.Errorf("%q on %q at %d: draft %q, want %q", tc.keys, tc.val, tc.from, got, tc.want)
		}
		if got := vimCursor(m); got != tc.cursor {
			t.Errorf("%q on %q at %d: cursor %d, want %d", tc.keys, tc.val, tc.from, got, tc.cursor)
		}
		if got := m.vimInsert(); got != tc.insert {
			t.Errorf("%q on %q: insert mode %v, want %v", tc.keys, tc.val, got, tc.insert)
		}
	}
}

// Normal mode never types; i goes back to typing; Esc returns to normal
// mode with the cursor on the last character typed; u undoes one command
// at a time.
func TestVimModesAndUndo(t *testing.T) {
	m := vimModel(t, "one two three", 0)
	vimKeys(m, "zq")
	if m.input.Value() != "one two three" {
		t.Fatalf("normal mode typed: %q", m.input.Value())
	}
	vimKeys(m, "A", " four")
	if got := m.input.Value(); got != "one two three four" {
		t.Fatalf("insert mode should type: %q", got)
	}
	vimKeys(m, "esc")
	if !m.vimNormal() || vimCursor(m) != len("one two three four")-1 {
		t.Fatalf("Esc should go to normal mode on the last character, normal=%v cursor=%d", m.vimNormal(), vimCursor(m))
	}
	vimKeys(m, "0", "dw", "dw")
	if got := m.input.Value(); got != "three four" {
		t.Fatalf("dw dw: %q", got)
	}
	vimKeys(m, "u")
	if got := m.input.Value(); got != "two three four" {
		t.Fatalf("u should undo one dw: %q", got)
	}
	vimKeys(m, "u")
	if got := m.input.Value(); got != "one two three four" {
		t.Fatalf("u again should undo the other: %q", got)
	}
	// An operator waits for its motion; Esc cancels it and stays in
	// normal mode.
	vimKeys(m, "d", "esc", "w")
	if got := m.input.Value(); got != "one two three four" || !m.vimNormal() {
		t.Fatalf("Esc should cancel d: %q normal=%v", got, m.vimNormal())
	}
}

// Esc in insert mode only goes to normal mode; in normal mode with an
// empty composer it is the usual Esc, which interrupts a running turn. The
// busy row says Esc Esc while insert mode would take the first one.
func TestVimEscKeepsInterrupt(t *testing.T) {
	m, clock := stClock(t)
	on := true
	m.cfg.Vim = &on
	stBegin(m, clock)
	cancelled := 0
	m.cancel = func() { cancelled++ }
	if got := stRow(m); !strings.Contains(got, "Esc Esc to interrupt") {
		t.Fatalf("insert mode, empty composer: row %q", got)
	}
	m.key(tea.KeyMsg{Type: tea.KeyEsc})
	if cancelled != 0 || !m.vimNormal() {
		t.Fatalf("the first Esc goes to normal mode only (cancelled %d, normal %v)", cancelled, m.vimNormal())
	}
	if got := stRow(m); !strings.Contains(got, "Esc to interrupt") || strings.Contains(got, "Esc Esc") {
		t.Fatalf("normal mode, empty composer: row %q", got)
	}
	m.key(tea.KeyMsg{Type: tea.KeyEsc})
	if cancelled != 1 {
		t.Fatalf("Esc in normal mode with an empty composer interrupts, cancelled %d", cancelled)
	}
}

// Off by default: Esc and letters behave as they always did.
func TestVimOffByDefault(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	m := compactCmdModel()
	m.Update(mkWinSize(100, 30))
	m.Update(keyRunes("x"))
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m.Update(keyRunes("j"))
	if got := m.input.Value(); got != "xj" {
		t.Fatalf("without vim mode keys type: %q", got)
	}
	if m.vimTag() != "" {
		t.Fatal("no mode indicator without vim mode")
	}
}

// /vim turns it on and saves it; the info row shows the mode.
func TestVimCommandAndIndicator(t *testing.T) {
	home := t.TempDir()
	t.Setenv("K_BRAIN_HOME", home)
	m := compactCmdModel()
	m.Update(mkWinSize(100, 30))
	m.command("/vim")
	if !m.vimEnabled() {
		t.Fatal("/vim should turn vim mode on")
	}
	b, err := os.ReadFile(filepath.Join(home, "config.json"))
	if err != nil || !strings.Contains(string(b), `"vim": true`) {
		t.Fatalf("/vim should save the setting: %v %s", err, b)
	}
	if row := ansi.Strip(m.infoRow()); !strings.Contains(row, "-- INSERT --") {
		t.Fatalf("info row: %q", row)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if row := ansi.Strip(m.infoRow()); !strings.Contains(row, "-- NORMAL --") {
		t.Fatalf("info row: %q", row)
	}
	m.command("/vim off")
	if m.vimEnabled() || m.vimTag() != "" {
		t.Fatal("/vim off should turn it off")
	}
}

// Enter in normal mode sends, and the next draft starts in insert mode.
func TestVimEnterSendsBackInInsert(t *testing.T) {
	m := vimModel(t, "", 0)
	m.command("/vim on")
	vimKeys(m, "i", "hi", "esc")
	if !m.vimNormal() {
		t.Fatal("want normal mode")
	}
	vimKeys(m, "enter")
	if m.vimNormal() {
		t.Fatal("after sending the composer should be in insert mode")
	}
}

// TestGoldenVimNormal pins the composer and info row in normal mode.
func TestGoldenVimNormal(t *testing.T) {
	t.Chdir(t.TempDir())
	m := vimModel(t, "fix the cache key\nthen rerun the tests", 4)
	m.layout()
	checkGolden(t, "vim_normal_100x30.txt", stripView(m.View()))
}
