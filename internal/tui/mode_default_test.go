package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

// A new install starts asking before commands and edits; an install from
// before the mode was saved keeps starting in "always"; a folder's own
// choice wins over both, and --cautious over everything for its run.
func TestInitialPermissionMode(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	wd, other := t.TempDir(), t.TempDir()
	saved := func(mode string) *config.Config { return &config.Config{PermissionMode: mode} }

	cases := []struct {
		name     string
		cfg      *config.Config
		dir      string
		cautious bool
		firstRun bool
		want     string
	}{
		{"existing install, nothing saved", saved(""), other, false, false, "always"},
		{"first run before setup saved", saved(""), other, false, true, "normal"},
		{"new install after setup", saved("normal"), other, false, false, "normal"},
		{"saved always stays", saved("always"), other, false, true, "always"},
		{"unknown value ignored", saved("bogus"), other, false, false, "always"},
		{"plan is not a starting mode", saved("plan"), other, false, false, "always"},
		{"cautious flag", saved("always"), other, true, false, "normal"},
	}
	for _, c := range cases {
		if got := initialPermissionMode(c.cfg, c.dir, c.cautious, c.firstRun); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}

	if err := config.SetProjectPermissionMode(wd, "auto_edit"); err != nil {
		t.Fatal(err)
	}
	if got := initialPermissionMode(saved("normal"), wd, false, false); got != "auto_edit" {
		t.Errorf("the folder's choice should win, got %q", got)
	}
	if got := initialPermissionMode(saved("normal"), other, false, false); got != "normal" {
		t.Errorf("another folder took this folder's choice: %q", got)
	}
	if got := initialPermissionMode(saved("normal"), wd, true, false); got != "normal" {
		t.Errorf("--cautious should start in normal, got %q", got)
	}
}

// Changing the mode saves it for the folder; plan, a detour, is not saved,
// and a model outside a real session saves nothing.
func TestPermissionModeChoiceSavedPerFolder(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	wd := t.TempDir()
	m := compactCmdModel()
	m.permissionMode = "normal"
	m.setPermissionMode("auto_edit")
	if got := config.ProjectPermissionMode(wd); got != "" {
		t.Fatalf("a model with no folder saved %q", got)
	}
	m.permWD = wd
	m.permissionCommand([]string{"full_access"})
	if got := config.ProjectPermissionMode(wd); got != "full_access" {
		t.Fatalf("/permissions full_access saved %q", got)
	}
	m.setPermissionMode("plan")
	if got := config.ProjectPermissionMode(wd); got != "full_access" {
		t.Fatalf("plan replaced the saved mode: %q", got)
	}
	m.cyclePermissionMode()
	if got := config.ProjectPermissionMode(wd); got != m.permissionMode {
		t.Fatalf("Shift+Tab to %q saved %q", m.permissionMode, got)
	}
}

// Setup gives a new install the "normal" mode and says so once; a mode the
// user already saved is left alone.
func TestSetupSavesNormalPermissionMode(t *testing.T) {
	saved := driveWizard(t, "\n")
	if saved.PermissionMode != "normal" {
		t.Fatalf("wizard saved permissionMode %q", saved.PermissionMode)
	}

	t.Setenv("K_BRAIN_HOME", t.TempDir())
	wd := t.TempDir()
	cfg := config.Default()
	o := newOnboardModel(wd, "", true, true)
	o.Update(mkWinSize(100, 30))
	if v := ansi.Strip(o.View()); !strings.Contains(v, "Normal mode") || !strings.Contains(v, "/permissions") {
		t.Fatalf("the welcome card does not mention the permission mode:\n%s", v)
	}
	o.choose(0)
	o.choose(0)
	o.choose(1)
	if ok, err := applyOnboarding(cfg, wd, o); !ok || err != nil {
		t.Fatalf("apply: %v %v", ok, err)
	}
	reloaded, err := config.Load()
	if err != nil || reloaded.PermissionMode != "normal" {
		t.Fatalf("onboarding saved permissionMode %q (%v)", reloaded.PermissionMode, err)
	}

	cfg = config.Default()
	cfg.PermissionMode = "always"
	setupPermissionDefault(cfg)
	if cfg.PermissionMode != "always" {
		t.Fatalf("setup overwrote a saved mode with %q", cfg.PermissionMode)
	}
}

// The mode chip is never cut, however narrow the terminal or long the
// model name: the rest of the row gives way.
func TestModeChipNeverTruncated(t *testing.T) {
	for _, mode := range []string{"normal", "always", "auto_review", "full_access"} {
		for _, w := range []int{100, 60, 40, 30, 24} {
			m := compactCmdModel()
			m.modelName = "a-very-long-model-name-that-fills-the-row-by-itself"
			m.permissionMode = mode
			m.Update(mkWinSize(w, 24))
			row := ansi.Strip(m.footerHints())
			label := m.permissionModeLabel()
			if !strings.Contains(row, label) {
				t.Errorf("%s at %d columns: chip %q cut from %q", mode, w, label, row)
			}
			if got := ansi.StringWidth(row); got > w {
				t.Errorf("%s at %d columns: row is %d wide", mode, w, got)
			}
		}
	}
}
