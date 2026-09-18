package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

func TestThemeAutoResolvesFromDetection(t *testing.T) {
	m := compactCmdModel()
	t.Setenv("K_BRAIN_THEME", "light")
	m.setTheme("dark")
	if CurrentTheme() != "dark" {
		t.Fatalf("explicit dark should win, got %q", CurrentTheme())
	}
	m.setTheme("auto")
	if CurrentTheme() != "light" {
		t.Fatalf("auto should resolve from detection (light), got %q", CurrentTheme())
	}
	setSchemeOverride("")
	SetLightTheme(false)
}

func TestThemeAutoNoteNamesSource(t *testing.T) {
	m := compactCmdModel()
	t.Setenv("K_BRAIN_THEME", "dark")
	m.setTheme("auto")
	var note string
	for _, b := range m.blocks {
		if strings.Contains(b.text, "◐ theme:") {
			note = b.text
		}
	}
	if !strings.Contains(note, "(auto: K_BRAIN_THEME)") {
		t.Fatalf("auto note should name the detection source, got %q", note)
	}
	setSchemeOverride("")
	SetLightTheme(false)
}

func TestConfigSyncAppliesThemeFromFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("K_BRAIN_HOME", dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	m := compactCmdModel()
	m.cfgMod = time.Now().Add(-time.Minute)
	m.setTheme("dark")
	delete(m.cfgExtra, "theme")
	m.cfg.Theme = "dark"

	other, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	other.Theme = "light"
	if err := other.Save(); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}

	m.applyCfgSync(cfgSyncMsg{mod: fi.ModTime(), theme: other.Theme})
	if m.cfg.Theme != "light" {
		t.Fatalf("synced theme should reach m.cfg, got %q", m.cfg.Theme)
	}
	if CurrentTheme() != "light" {
		t.Fatalf("synced theme should apply live, got %q", CurrentTheme())
	}
	setSchemeOverride("")
	SetLightTheme(false)
}

func TestConfigSyncRespectsPinnedTheme(t *testing.T) {
	m := compactCmdModel()
	m.setTheme("dark")

	m.applyCfgSync(cfgSyncMsg{mod: time.Now(), theme: "light"})
	if CurrentTheme() != "dark" {
		t.Fatalf("a pinned theme must survive another session's save, got %q", CurrentTheme())
	}
	if m.cfg.Theme != "dark" {
		t.Fatalf("m.cfg.Theme should stay dark, got %q", m.cfg.Theme)
	}
	setSchemeOverride("")
	SetLightTheme(false)
}

func TestThemeAutoUnpinsSync(t *testing.T) {
	m := compactCmdModel()
	m.setTheme("dark")
	m.setTheme("auto")
	if _, pinned := m.cfgExtra["theme"]; pinned {
		t.Fatal("auto must unpin the theme for the config watcher")
	}
	m.applyCfgSync(cfgSyncMsg{mod: time.Now(), theme: "dark"})
	if CurrentTheme() != "dark" {
		t.Fatalf("after auto, a file change should sync again, got %q", CurrentTheme())
	}
	setSchemeOverride("")
	SetLightTheme(false)
}

func TestConfigSyncIgnoresOwnSaves(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("K_BRAIN_HOME", dir)
	m := compactCmdModel()
	if err := m.cfg.Save(); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	m.cfgMod = fi.ModTime()
	m.setTheme("dark")
	delete(m.cfgExtra, "theme")
	m.cfg.Theme = ""

	if _, cmd := m.cfgSync(); cmd != nil {

		_ = cmd
	}
	if m.cfg.Theme != "" {
		t.Fatalf("no newer file: theme must stay %q, got %q", "", m.cfg.Theme)
	}
	setSchemeOverride("")
	SetLightTheme(false)
}
