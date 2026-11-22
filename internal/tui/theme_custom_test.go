package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/muesli/termenv"
)

// withCustomThemes points the config directory at a temp dir with the given
// theme files and loads them; the cleanup drops them again.
func withCustomThemes(t *testing.T, files map[string]string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("K_BRAIN_HOME", home)
	dir, err := themesDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reloadCustomThemes(true)
	t.Cleanup(func() {
		customThemes, customThemeErrs, customThemeSig = nil, nil, ""
		if activeTheme.source != "" {
			activateTheme(builtinThemes[0])
		}
	})
	return dir
}

func TestParseThemeFile(t *testing.T) {
	th, err := parseThemeFile("Ocean", []byte(`{
		"description": "deep blue",
		"base": "harbor",
		"dark": {"accent": "#4FC3F7", "state.ok": {"hex": "#7fd18b", "x256": 114, "ansi": 10}, "surface.band": "#10202c"},
		"light": {"accent": {"hex": "#0277bd"}}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if th.name != "ocean" || th.desc != "deep blue" || th.dark == nil || th.light == nil {
		t.Fatalf("parsed %+v", th)
	}
	harbor := lookupTheme("harbor")
	if got := th.dark[tokAccent]; got.hex != "#4fc3f7" || got.x256 == "" || got.ansi == "" || got.ansi == "0" {
		t.Errorf("computed fallbacks: %+v", got)
	}
	if got := th.dark[tokStateOK]; got != (themeColor{"#7fd18b", "114", "10"}) {
		t.Errorf("explicit fallbacks: %+v", got)
	}
	if got := th.dark[tokSurfaceBand]; got.ansi != "" {
		t.Errorf("a surface must not get a 16-colour value: %+v", got)
	}
	if th.dark[tokStateWarn] != harbor.dark[tokStateWarn] || th.light[tokStateError] != harbor.light[tokStateError] {
		t.Error("tokens the file does not set should come from the base")
	}

	// No base means the default theme, both variants; a one-variant base
	// gives a one-variant theme.
	def, err := parseThemeFile("plain", []byte(`{}`))
	if err != nil || def.dark == nil || def.light == nil || def.desc != "based on default" {
		t.Fatalf("empty file: %+v, %v", def, err)
	}
	dusky, err := parseThemeFile("dusky", []byte(`{"base": "dusk", "dark": {"accent": "#ffb86b"}}`))
	if err != nil || dusky.polarity() != "dark" {
		t.Fatalf("dusk-based theme: %+v, %v", dusky, err)
	}
}

// The word-emphasis tints of paired diff rows are ordinary surface tokens:
// a file can set them per variant, and the rest come from the base.
func TestCustomThemeDiffEmphasis(t *testing.T) {
	th, err := parseThemeFile("tinted", []byte(`{
		"base": "harbor",
		"dark": {"diff.add.emph.bg": "#20603a", "diff.del.emph.bg": {"hex": "#702830", "x256": 88}}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	harbor := lookupTheme("harbor")
	if got := th.dark[tokDiffAddEmphBG]; got.hex != "#20603a" || got.x256 == "" || got.ansi != "" {
		t.Errorf("diff.add.emph.bg: %+v", got)
	}
	if got := th.dark[tokDiffDelEmphBG]; got != (themeColor{"#702830", "88", ""}) {
		t.Errorf("diff.del.emph.bg: %+v", got)
	}
	if th.light[tokDiffAddEmphBG] != harbor.light[tokDiffAddEmphBG] || th.light[tokDiffDelEmphBG] != harbor.light[tokDiffDelEmphBG] {
		t.Error("the light variant should keep the base's emphasis tints")
	}
	if w := themeContrastWarnings(th); len(w) != 0 {
		t.Errorf("readable tints should not warn: %q", w)
	}
	pale, err := parseThemeFile("pale", []byte(`{"dark": {"diff.del.emph.bg": "#c0a0a0"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if w := strings.Join(themeContrastWarnings(pale), "\n"); !strings.Contains(w, "dark text.secondary is") || !strings.Contains(w, "on diff.del.emph.bg") {
		t.Errorf("a pale tint under light text should warn, got %q", w)
	}
	if _, err := parseThemeFile("tinted", []byte(`{"dark": {"diff.add.emph.bg": {"hex": "#20603a", "ansi": 2}}}`)); err == nil || !strings.Contains(err.Error(), "a surface takes no ansi value") {
		t.Errorf("an ansi value on an emphasis tint: %v", err)
	}

	defer activateTheme(builtinThemes[0])
	defer SetLightTheme(false)
	restore := useProfile(t, termenv.TrueColor, true)
	defer restore()
	activateTheme(th)
	SetLightTheme(false)
	rows := strings.Split(renderDiff("4 - \tv := cache.Get(key, width)\n4 + \tv := cache.Get(key, height)", "main.go", 80), "\n")
	for _, c := range []struct{ row, word, hex string }{{rows[0], "width", "#702830"}, {rows[1], "height", "#20603a"}} {
		i := strings.Index(c.row, c.word)
		if i < 0 || !sgrBGNear(c.row[strings.LastIndex(c.row[:i], "\x1b["):i], c.hex) {
			t.Errorf("%q should sit on %s in %q", c.word, c.hex, c.row)
		}
	}
}

func TestParseThemeFileErrors(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`{"dark": {"accent": "#4fc3f7"}`, "invalid JSON"},
		{`{"colors": {}}`, `unknown field "colors"`},
		{`{} {}`, "more than one value"},
		{`{"name": "Bad Name"}`, "lower-case letters"},
		{`{"name": "auto"}`, "reserved"},
		{`{"name": "harbor"}`, "built-in theme"},
		{`{"base": "neon"}`, `base "neon" is not a built-in theme`},
		{`{"base": "dusk", "light": {"accent": "#000000"}}`, `light: base "dusk" has no light variant`},
		{`{"dark": {"accentt": "#4fc3f7"}}`, "dark.accentt: unknown token"},
		{`{"dark": {"text.primary": "#ffffff"}}`, "dark.text.primary: unknown token"},
		{`{"dark": {"accent": "teal"}}`, `dark.accent: "teal" is not a #rrggbb colour`},
		{`{"dark": {"accent": "#4fc3f"}}`, "is not a #rrggbb colour"},
		{`{"dark": {"accent": 12}}`, `dark.accent: want "#rrggbb" or`},
		{`{"dark": {"accent": {"hex": "#4fc3f7", "rgb": 1}}}`, `want "#rrggbb" or`},
		{`{"dark": {"accent": {"hex": "#4fc3f7", "x256": 300}}}`, "x256 300 is not in 0..255"},
		{`{"light": {"accent": {"hex": "#4fc3f7", "ansi": 16}}}`, "light.accent: ansi 16 is not in 0..15"},
		{`{"dark": {"surface.band": {"hex": "#10202c", "ansi": 0}}}`, "a surface takes no ansi value"},
	} {
		_, err := parseThemeFile("custom", []byte(tc.body))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %v, want it to mention %q", tc.body, err, tc.want)
		}
	}
}

// Contrast below the built-in minimums loads, with a warning per finding.
func TestCustomThemeContrastWarnings(t *testing.T) {
	th, err := parseThemeFile("murky", []byte(`{"dark": {"accent": "#303030", "text.muted": "#444444"}}`))
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(th.warnings, "\n")
	for _, want := range []string{"dark accent #303030", "dark text.muted #444444", "want 4.5:1"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings %q should mention %q", joined, want)
		}
	}
	if strings.Contains(joined, "light ") {
		t.Errorf("the untouched light variant should not warn: %q", joined)
	}
	ok, _ := parseThemeFile("fine", []byte(`{"base": "harbor"}`))
	if len(ok.warnings) != 0 {
		t.Errorf("an unchanged built-in base should not warn: %v", ok.warnings)
	}
}

func TestNearest16AvoidsBackgroundSlot(t *testing.T) {
	if got := nearest16("#101010", false); got == "0" {
		t.Errorf("a near-black foreground on a dark terminal got slot 0")
	}
	if got := nearest16("#fafafa", true); got == "15" || got == "7" {
		t.Errorf("a near-white foreground on a light terminal got slot %s", got)
	}
	if got := nearest16("#ff2020", false); got != "9" {
		t.Errorf("bright red: slot %s", got)
	}
}

// Files load in name order; a broken one is skipped with an error naming it,
// a duplicate name loses to the earlier file, and the rest are offered.
func TestLoadCustomThemesFromDir(t *testing.T) {
	withCustomThemes(t, map[string]string{
		"a-ocean.json": `{"name": "ocean", "base": "harbor", "dark": {"accent": "#4fc3f7"}}`,
		"b-ocean.json": `{"name": "ocean"}`,
		"broken.json":  `{"dark": {"accent": "teal"}}`,
		"notes.txt":    `not a theme`,
		"sunset.json":  `{"base": "paper", "light": {"accent": "#a33d1f"}}`,
	})
	if got := themeChoices(); strings.Join(got, ",") != "auto,light,dark,dusk,harbor,paper,contrast,ocean,sunset,mono" {
		t.Errorf("choices: %v", got)
	}
	errs := strings.Join(customThemeErrors(), "\n")
	for _, want := range []string{
		filepath.Join("themes", "b-ocean.json") + `: name "ocean" is already used`,
		filepath.Join("themes", "broken.json") + `: dark.accent: "teal" is not a #rrggbb colour`,
	} {
		if !strings.Contains(errs, want) {
			t.Errorf("errors %q should contain %q", errs, want)
		}
	}
	if ch, ok := parseThemeSetting("sunset"); !ok || ch.pin != "light" {
		t.Errorf("sunset is paper-based, so light only: %+v %v", ch, ok)
	}
	if d := themeChoiceDesc("ocean"); d != "custom · based on harbor" {
		t.Errorf("desc %q", d)
	}
}

// /theme picks a custom theme; saving the file again re-applies it, and
// deleting it falls back to the default theme without losing the setting.
func TestCustomThemeHotReload(t *testing.T) {
	restore := useProfile(t, termenv.TrueColor, true)
	defer restore()
	m := surfaceModel(t)
	dir := withCustomThemes(t, map[string]string{"ocean.json": `{"base": "harbor", "dark": {"accent": "#4fc3f7"}}`})
	defer m.setTheme("dark")

	m.command("/theme ocean:dark")
	if activeTheme.name != "ocean" || tokenTable[tokAccent].dark.hex != "#4fc3f7" {
		t.Fatalf("active %q accent %s", activeTheme.name, tokenTable[tokAccent].dark.hex)
	}
	path := filepath.Join(dir, "ocean.json")
	if err := os.WriteFile(path, []byte(`{"base": "harbor", "dark": {"accent": "#81d4fa"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	m.refreshCustomThemes(false)
	if tokenTable[tokAccent].dark.hex != "#81d4fa" {
		t.Fatalf("an edited file should re-apply: accent %s", tokenTable[tokAccent].dark.hex)
	}
	if !strings.Contains(accentFgStyle.Render("x"), "38;2;129;21") {
		t.Errorf("styles should be rebuilt with the new accent: %q", accentFgStyle.Render("x"))
	}

	if err := os.WriteFile(path, []byte(`{"base": "harbor", "dark": {"accent": "blue"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	m.refreshCustomThemes(false)
	if activeTheme.name != defaultThemeName || m.cfg.Theme != "ocean:dark" {
		t.Errorf("a broken file falls back to the default theme and keeps the setting: active %q saved %q", activeTheme.name, m.cfg.Theme)
	}
	if v := surfScreen(m); !strings.Contains(v, "theme file skipped: "+filepath.Join("themes", "ocean.json")) {
		t.Errorf("the transcript should say which file was skipped:\n%s", v)
	}
}

// The picker lists custom themes after the built-ins, with skipped files and
// the previewed theme's contrast warning above the sample.
func TestThemePickerCustomGolden(t *testing.T) {
	restore := useProfile(t, termenv.TrueColor, true)
	defer restore()
	t.Setenv("K_BRAIN_THEME", "dark")
	m := surfaceModel(t)
	withCustomThemes(t, map[string]string{
		"murky.json":  `{"description": "low-contrast experiment", "dark": {"accent": "#303030"}}`,
		"ocean.json":  `{"description": "deep blue", "base": "harbor", "dark": {"accent": "#4fc3f7"}}`,
		"broken.json": `{"dark": {"accent": "teal"}}`,
	})
	m.setTheme("dark")
	defer m.setTheme("dark")
	m.command("/theme")
	pp := m.palette.top()
	for pp.list[pp.midx] != "murky" {
		m.paletteKey(tea.KeyMsg{Type: tea.KeyDown})
	}
	view := m.View()
	checkGolden(t, "surface-theme-custom_truecolor-dark_80x24.ansi", view)
	checkGolden(t, "surface-theme-custom_truecolor-dark_80x24.txt", stripView(view))
}
