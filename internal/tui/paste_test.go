package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"
)

func TestPastedScreenshotPathAttachesImage(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())

	source := filepath.Join(t.TempDir(), "Screenshot")
	image := []byte("\x89PNG\r\n\x1a\nimage-data")
	if err := os.WriteFile(source, image, 0o600); err != nil {
		t.Fatal(err)
	}

	m := compactCmdModel()
	tm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(source + "\n"), Paste: true})
	m = tm.(*model)
	if cmd == nil {
		t.Fatal("pasted screenshot path should schedule an attachment")
	}

	tm, _ = m.Update(cmd())
	m = tm.(*model)

	if !strings.Contains(m.input.Value(), "[Image 1: Screenshot"+chipSentinel+"]") {
		t.Fatalf("input should show a chip, got %q", m.input.Value())
	}

	if len(m.images) != 1 || m.images[0].n != 1 {
		t.Fatalf("expected one session image, got %+v", m.images)
	}
	data, err := os.ReadFile(m.images[0].path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(image) {
		t.Fatalf("saved image = %q, want %q", data, image)
	}
}

func TestSaveClipboardImageStaysWithinPasteDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("K_BRAIN_HOME", home)

	if _, err := saveClipboardImage("../../escaped", []byte("image")); err == nil {
		t.Fatal("path-traversing extension should be rejected")
	}
	if _, err := os.Stat(filepath.Join(home, "escaped")); !os.IsNotExist(err) {
		t.Fatalf("write escaped the paste directory: %v", err)
	}
}

func TestPasteChipOnByDefault(t *testing.T) {
	paste := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("line1\nline2\nline3\nline4"), Paste: true}

	m := compactCmdModel()
	m.Update(paste)
	chip := "[Pasted 4 lines" + chipSentinel + "]"
	if m.input.Value() != chip {
		t.Fatalf("a large paste should become one chip by default, got %q", m.input.Value())
	}
	if got := m.expandPasteChips(m.input.Value()); got != "line1\nline2\nline3\nline4" {
		t.Fatalf("send should expand the chip, got %q", got)
	}

	off := false
	m2 := compactCmdModel()
	m2.cfg.CollapsePaste = &off
	m2.Update(paste)
	if !strings.Contains(m2.input.Value(), "line1\nline2") || strings.Contains(m2.input.Value(), "[Pasted") {
		t.Fatalf("collapsePaste=false should paste verbatim, got %q", m2.input.Value())
	}
}

func TestPasteCollapseShortPasteIgnored(t *testing.T) {
	on := true
	m := compactCmdModel()
	m.cfg.CollapsePaste = &on
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("just one line"), Paste: true})
	if strings.Contains(m.input.Value(), "[Pasted") {
		t.Fatal("a one-line paste should not collapse")
	}
}

func TestImageChipRendering(t *testing.T) {

	anon := pastedImage{n: 2, path: "/tmp/copy.png", display: ""}
	if got := anon.chipText(); got != "[Image 2"+chipSentinel+"]" {
		t.Fatalf("anonymous chip = %q, want %q", got, "[Image 2"+chipSentinel+"]")
	}

	short := pastedImage{n: 1, path: "/tmp/copy.png", display: "shot.png"}
	if got := short.chipText(); got != "[Image 1: shot.png"+chipSentinel+"]" {
		t.Fatalf("short chip = %q, want %q", got, "[Image 1: shot.png"+chipSentinel+"]")
	}

	long := pastedImage{n: 3, path: "/tmp/copy.png", display: "Screenshot 2026-09-04 at 10.21.33 AM.png"}
	got := long.chipText()
	if strings.Contains(got, "10.21.33") || !strings.HasSuffix(got, ".png"+chipSentinel+"]") || !strings.Contains(got, "…") {
		t.Fatalf("long chip = %q, want a truncated stem + ellipsis + .png", got)
	}

	inner := strings.TrimSuffix(strings.TrimPrefix(got, "[Image 3: "), chipSentinel+"]")
	if runewidth.StringWidth(inner) > maxImageNameRunes {
		t.Fatalf("chip snippet %q exceeds the %d-rune budget", inner, maxImageNameRunes)
	}
}

func TestImageChipsNumberAndResolve(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())

	src1 := filepath.Join(t.TempDir(), "first.png")
	src2 := filepath.Join(t.TempDir(), "second.png")
	if err := os.WriteFile(src1, []byte("\x89PNG\r\n\x1a\none"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src2, []byte("\x89PNG\r\n\x1a\ntwo"), 0o600); err != nil {
		t.Fatal(err)
	}

	m := compactCmdModel()
	paste := func(src string) {
		tm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(src + "\n"), Paste: true})
		m = tm.(*model)
		tm, _ = m.Update(cmd())
		m = tm.(*model)
	}
	paste(src1)
	paste(src2)

	if !strings.Contains(m.input.Value(), "[Image 1: first.png"+chipSentinel+"]") || !strings.Contains(m.input.Value(), "[Image 2: second.png"+chipSentinel+"]") {
		t.Fatalf("chips should number 1 and 2, got %q", m.input.Value())
	}
	if m.imageSeq != 2 || len(m.images) != 2 {
		t.Fatalf("session image bookkeeping: seq=%d len=%d", m.imageSeq, len(m.images))
	}

	expanded := m.expandImageChips(m.input.Value())
	if !strings.Contains(expanded, "@"+m.images[0].path) || !strings.Contains(expanded, "@"+m.images[1].path) {
		t.Fatalf("chips should expand to their @paths, got %q", expanded)
	}
	if !strings.Contains(m.input.Value(), "[Image 1:") || !strings.Contains(m.input.Value(), "[Image 2:") {
		t.Fatal("expandImageChips mutating the input value")
	}

	if got := m.expandImageChips("[Image 99: nope.png" + chipSentinel + "]"); got != "[Image 99: nope.png"+chipSentinel+"]" {
		t.Fatalf("unknown chip should stay literal, got %q", got)
	}

	if got := m.expandImageChips("[Image 1]"); got != "[Image 1]" {
		t.Fatalf("hand-typed [Image 1] without sentinel should stay literal, got %q", got)
	}

	if got := m.expandImageChips("[Image 1: fake.png]"); got != "[Image 1: fake.png]" {
		t.Fatalf("hand-typed chip without sentinel should stay literal, got %q", got)
	}
}

func TestTempImageCopiedBeforeSourceVanishes(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())

	source := filepath.Join(t.TempDir(), "Screenshot temp")
	image := []byte("\x89PNG\r\n\x1a\nhover-image")
	if err := os.WriteFile(source, image, 0o600); err != nil {
		t.Fatal(err)
	}

	m := compactCmdModel()
	tm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(source + "\n"), Paste: true})
	m = tm.(*model)
	tm, _ = m.Update(cmd())
	m = tm.(*model)

	if len(m.images) != 1 {
		t.Fatalf("expected one pasted image, got %d", len(m.images))
	}
	copied := m.images[0].path
	if copied == source {
		t.Fatal("the paste must copy out of the transient staging dir, not reference it")
	}

	if !strings.Contains(m.input.Value(), "[Image 1: Screenshot temp"+chipSentinel+"]") {
		t.Fatalf("chip should carry the staging display name, got %q", m.input.Value())
	}

	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(copied)
	if err != nil {
		t.Fatalf("copied image should survive the source vanishing: %v", err)
	}
	if string(got) != string(image) {
		t.Fatalf("copied image = %q, want %q", got, image)
	}

	if !strings.Contains(m.expandImageChips(m.input.Value()), "@"+copied) {
		t.Fatalf("chip should resolve to the surviving copy, got %q", m.expandImageChips(m.input.Value()))
	}
}
