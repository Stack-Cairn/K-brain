package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/muesli/termenv"
)

func TestParseThemeSetting(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want themeChoice
		ok   bool
	}{
		{"", themeChoice{family: "default"}, true},
		{"auto", themeChoice{family: "default"}, true},
		{"Light", themeChoice{family: "default", pin: "light"}, true},
		{"dark", themeChoice{family: "default", pin: "dark"}, true},
		{"mono", themeChoice{family: "default", mono: true}, true},
		{"harbor", themeChoice{family: "harbor"}, true},
		{"harbor:light", themeChoice{family: "harbor", pin: "light"}, true},
		// A one-variant family is always that variant.
		{"dusk", themeChoice{family: "dusk", pin: "dark"}, true},
		{"paper", themeChoice{family: "paper", pin: "light"}, true},
		{"paper:dark", themeChoice{family: "paper", pin: "light"}, true},
		{"harbor:dim", themeChoice{family: "default"}, false},
		{"neon", themeChoice{family: "default"}, false},
	} {
		got, ok := parseThemeSetting(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("parseThemeSetting(%q) = %+v, %v; want %+v, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

// The built-in set covers at least three dark and two light variants, a
// high-contrast family, and every family has a description.
func TestBuiltinThemeSet(t *testing.T) {
	dark, light, hc := 0, 0, 0
	for _, th := range builtinThemes {
		if th.dark != nil {
			dark++
		}
		if th.light != nil {
			light++
		}
		if th.highContrast {
			hc++
		}
		if th.desc == "" {
			t.Errorf("%s has no description", th.name)
		}
	}
	if dark < 3 || light < 2 || hc < 1 {
		t.Errorf("built-ins: %d dark, %d light, %d high-contrast variants", dark, light, hc)
	}
}

// Switching to a named theme repaints with that theme's colours; the default
// theme's columns come back unchanged afterwards.
func TestNamedThemeSwitchesPalette(t *testing.T) {
	restore := useProfile(t, termenv.ANSI256, true)
	defer restore()
	m := surfaceModel(t)
	defer m.setTheme("dark")

	m.command("/theme harbor:light")
	if m.cfg.Theme != "harbor:light" || activeTheme.name != "harbor" || CurrentTheme() != "light" {
		t.Fatalf("theme %q active %q current %q", m.cfg.Theme, activeTheme.name, CurrentTheme())
	}
	if got, want := accentFgStyle.Render("x"), "38;5;"+lookupTheme("harbor").light[tokAccent].x256; !strings.Contains(got, want) {
		t.Errorf("harbor light accent: %q, want %s", got, want)
	}

	m.command("/theme paper")
	if CurrentTheme() != "light" {
		t.Errorf("paper is light-only, got %q", CurrentTheme())
	}
	m.command("/theme dusk")
	if CurrentTheme() != "dark" || tokenTable[tokAccent].light != tokenTable[tokAccent].dark {
		t.Errorf("dusk is dark-only and uses its one variant for both columns")
	}

	m.command("/theme dark")
	if tokenTable != defaultTokens {
		t.Error("the default theme should restore the default token table exactly")
	}
}

// Moving through the picker previews each theme without saving it; Esc puts
// the saved theme back and Enter saves the one under the cursor.
func TestThemePickerPreviewsAndReverts(t *testing.T) {
	t.Setenv("K_BRAIN_THEME", "dark")
	restore := useProfile(t, termenv.ANSI256, true)
	defer restore()
	m := surfaceModel(t)
	m.setTheme("dark")
	defer m.setTheme("dark")

	m.command("/theme")
	pp := m.palette.top()
	if pp == nil || pp.kind != panelTheme || pp.list[pp.midx] != "dark" {
		t.Fatalf("picker should open on the saved theme")
	}
	m.paletteKey(tea.KeyMsg{Type: tea.KeyDown})
	if activeTheme.name != "dusk" || m.cfg.Theme != "dark" {
		t.Fatalf("down should preview dusk without saving: active %q saved %q", activeTheme.name, m.cfg.Theme)
	}
	if v := surfScreen(m); !strings.Contains(v, "amber accent") || !strings.Contains(v, "func total") {
		t.Errorf("picker should describe the themes and show a sample:\n%s", v)
	}
	m.paletteKey(tea.KeyMsg{Type: tea.KeyEsc})
	if activeTheme.name != "default" || m.cfg.Theme != "dark" || m.palette != nil {
		t.Fatalf("esc should restore the saved theme: active %q saved %q", activeTheme.name, m.cfg.Theme)
	}

	m.command("/theme")
	m.paletteKey(tea.KeyMsg{Type: tea.KeyDown})
	m.paletteKey(tea.KeyMsg{Type: tea.KeyDown})
	m.paletteKey(tea.KeyMsg{Type: tea.KeyEnter})
	if m.cfg.Theme != "harbor" || activeTheme.name != "harbor" {
		t.Fatalf("enter should save harbor: saved %q active %q", m.cfg.Theme, activeTheme.name)
	}
}

// The settings row cycles through every theme and opens the picker on Enter.
func TestSettingsThemeRowCyclesAllThemes(t *testing.T) {
	t.Setenv("K_BRAIN_THEME", "dark")
	m := surfaceModel(t)
	m.setTheme("dark")
	defer m.setTheme("dark")
	var row settingDef
	for _, d := range settingDefs() {
		if d.name == "Theme" {
			row = d
		}
	}
	row.step(m, 1)
	if m.cfg.Theme != "dusk" {
		t.Fatalf("→ from dark should pick dusk, got %q", m.cfg.Theme)
	}
	row.step(m, -1)
	if m.cfg.Theme != "dark" {
		t.Fatalf("← should go back to dark, got %q", m.cfg.Theme)
	}
	row.open(m)
	if m.palette == nil || m.palette.top() == nil || m.palette.top().kind != panelTheme {
		t.Fatal("Enter on the theme row should open the picker")
	}
}

// TestThemeGoldens pins one representative screen (a turn with tool rows,
// code and a diff) in every variant of every built-in theme other than the
// default, whose views TestGoldenViews already pins.
func TestThemeGoldens(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "src", "acme", "kn")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	prevNow := toolTimer.now
	defer func() { toolTimer.now = prevNow }()
	defer SetLightTheme(false)

	for _, th := range builtinThemes[1:] {
		for _, light := range []bool{false, true} {
			if th.variant(light) == nil || (light && th.light == nil) || (!light && th.dark == nil) {
				continue
			}
			variant := map[bool]string{false: "dark", true: "light"}[light]
			name := fmt.Sprintf("theme-%s-%s_truecolor_80x24", th.name, variant)
			t.Run(name, func(t *testing.T) {
				restore := useProfile(t, termenv.TrueColor, !light)
				defer restore()
				activateTheme(th)
				SetLightTheme(light)
				setASCIIGlyphs(false)

				clock := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
				toolTimer.now = func() time.Time { return clock }
				m := compactCmdModel()
				m.now = func() time.Time { return clock }
				m.permissionMode = "always"
				m.applyAppearance()
				m.Update(mkWinSize(80, 24))
				goldenTurn(m, &clock)
				m.layout()
				view := m.View()
				checkGolden(t, name+".ansi", view)
				checkGolden(t, name+".txt", stripView(view))
			})
		}
	}
}
