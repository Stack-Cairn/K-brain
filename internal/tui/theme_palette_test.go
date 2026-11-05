package tui

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// Every token is filled in for both backgrounds and every colour depth:
// lipgloss picks one value per profile and an empty one silently renders
// uncoloured. Surfaces are the exception on 16 colours, where a tint would
// collide with the terminal's own palette, and text.primary, which is always
// the terminal's default foreground.
func TestTokensCoverEveryColumn(t *testing.T) {
	names := map[string]bool{}
	for id := tokenID(0); id < tokenCount; id++ {
		d := tokenTable[id]
		if d.name == "" {
			t.Fatalf("token %d has no name", id)
		}
		if names[d.name] {
			t.Errorf("token name %q is used twice", d.name)
		}
		names[d.name] = true
		if d.termDefault {
			if d.dark != (themeColor{}) || d.light != (themeColor{}) {
				t.Errorf("%s keeps the terminal foreground and must not carry colours", d.name)
			}
			continue
		}
		for mode, c := range map[string]themeColor{"dark": d.dark, "light": d.light} {
			if !regexp.MustCompile(`^#[0-9a-f]{6}$`).MatchString(c.hex) {
				t.Errorf("%s/%s: truecolor %q is not a 6-digit lower-case hex", mode, d.name, c.hex)
			}
			check := func(label, v string, limit int) {
				n, err := strconv.Atoi(v)
				if err != nil || n < 0 || n > limit {
					t.Errorf("%s/%s: %s value %q is not in 0..%d", mode, d.name, label, v, limit)
				}
			}
			check("256-colour", c.x256, 255)
			if d.surface {
				if c.ansi != "" {
					t.Errorf("%s/%s: a surface must not tint 16-colour terminals, got %q", mode, d.name, c.ansi)
				}
				continue
			}
			check("16-colour", c.ansi, 15)
		}
	}
}

// relLuminance is the WCAG relative luminance of a #rrggbb colour.
func relLuminance(t *testing.T, hex string) float64 {
	t.Helper()
	var r, g, b int
	if _, err := fmt.Sscanf(hex, "#%02x%02x%02x", &r, &g, &b); err != nil {
		t.Fatalf("bad hex %q", hex)
	}
	lin := func(v int) float64 {
		c := float64(v) / 255
		if c <= 0.03928 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(r) + 0.7152*lin(g) + 0.0722*lin(b)
}

func contrast(t *testing.T, a, b string) float64 {
	la, lb := relLuminance(t, a), relLuminance(t, b)
	return (max(la, lb) + 0.05) / (min(la, lb) + 0.05)
}

// Text tokens meet WCAG AA for body text (4.5:1) and every other foreground
// meets the non-text minimum (3:1), on a typical dark terminal and on white.
// Surfaces, rules and diff tints are backgrounds and decoration, so they are
// checked through the text that sits on them instead.
func TestTokenContrast(t *testing.T) {
	const darkBG, lightBG = "#1e1e1e", "#ffffff"
	for id := tokenID(0); id < tokenCount; id++ {
		d := tokenTable[id]
		if d.termDefault || d.surface || id == tokLineRule || id == tokChipInk {
			continue
		}
		minimum := 3.0
		if strings.HasPrefix(d.name, "text.") {
			minimum = 4.5
		}
		if got := contrast(t, d.dark.hex, darkBG); got < minimum {
			t.Errorf("%s dark %s on %s: %.2f:1, want >= %.1f:1", d.name, d.dark.hex, darkBG, got, minimum)
		}
		if got := contrast(t, d.light.hex, lightBG); got < minimum {
			t.Errorf("%s light %s on %s: %.2f:1, want >= %.1f:1", d.name, d.light.hex, lightBG, got, minimum)
		}
	}
	// Text that sits on a surface or a chip.
	for _, pair := range []struct {
		fg, bg  tokenID
		minimum float64
	}{
		{tokTextSecondary, tokSurfaceBand, 4.5},
		{tokTextMuted, tokSurfaceBand, 4.5},
		{tokAccent, tokSurfaceRaised, 3},
		{tokChipInk, tokStateError, 3},
		{tokChipInk, tokAccent, 3},
		{tokChipInk, tokStateWarn, 3},
		{tokTextSecondary, tokLineRule, 4.5},
	} {
		fg, bg := tokenTable[pair.fg], tokenTable[pair.bg]
		if got := contrast(t, fg.dark.hex, bg.dark.hex); got < pair.minimum {
			t.Errorf("dark %s on %s: %.2f:1, want >= %.1f:1", fg.name, bg.name, got, pair.minimum)
		}
		if got := contrast(t, fg.light.hex, bg.light.hex); got < pair.minimum {
			t.Errorf("light %s on %s: %.2f:1, want >= %.1f:1", fg.name, bg.name, got, pair.minimum)
		}
	}
}

// ansi16SGR is the escape parameter termenv emits for a 16-colour index: the
// low eight are 30-37, the bright eight are 90-97.
func ansi16SGR(idx string) string {
	n, err := strconv.Atoi(idx)
	if err != nil {
		return ""
	}
	if n < 8 {
		return strconv.Itoa(30 + n)
	}
	return strconv.Itoa(90 + n - 8)
}

// The hand-picked fallbacks exist so a 256- or 16-colour terminal keeps the
// palette's meaning instead of a computed downsample.
func TestPaletteResolvesPerProfile(t *testing.T) {
	for _, tc := range []struct {
		profile termenv.Profile
		dark    bool
		want    func(themeColor) string
	}{
		{termenv.TrueColor, true, func(c themeColor) string {
			r, _ := strconv.ParseUint(c.hex[1:3], 16, 8)
			g, _ := strconv.ParseUint(c.hex[3:5], 16, 8)
			b, _ := strconv.ParseUint(c.hex[5:7], 16, 8)
			return fmt.Sprintf("38;2;%d;%d;%d", r, g, b)
		}},
		{termenv.ANSI256, true, func(c themeColor) string { return "38;5;" + c.x256 }},
		{termenv.ANSI256, false, func(c themeColor) string { return "38;5;" + c.x256 }},
		{termenv.ANSI, true, func(c themeColor) string { return ansi16SGR(c.ansi) }},
	} {
		restore := useProfile(t, tc.profile, tc.dark)
		got := accentFgStyle.Render("x")
		c := tokenTable[tokAccent].dark
		if !tc.dark {
			c = tokenTable[tokAccent].light
		}
		if want := tc.want(c); !strings.Contains(got, want) {
			t.Errorf("profile %v dark=%v: accent rendered %q, want a %q sequence", tc.profile, tc.dark, got, want)
		}
		restore()
	}
}

// On a 16-colour terminal a surface tint would be one of the user's own palette
// slots, so bands drop out and the rail carries the structure instead.
func TestSurfacesDropOutOn16Colours(t *testing.T) {
	restore := useProfile(t, termenv.ANSI, true)
	defer restore()
	if backgroundsActive() {
		t.Fatal("16 colours should not draw surfaces")
	}
	if out := bandStyle.Render("x"); strings.Contains(out, "\x1b[") {
		t.Errorf("the band should render untinted on 16 colours: %q", out)
	}
	if out := renderUserBand("hi", 20); !strings.Contains(ansi.Strip(out), glyphs.Band+glyphs.User+" hi") {
		t.Errorf("without a tint the user row keeps its rail: %q", ansi.Strip(out))
	}
}

// Errors and warnings must not collapse into the accent, or "something is wrong"
// and "here is the action" become indistinguishable.
func TestSemanticColoursAreDistinct(t *testing.T) {
	roles := []tokenID{tokAccent, tokStateOK, tokStateWarn, tokStateError, tokStateRunning, tokRoleAssistant}
	for _, mode := range []string{"dark", "light"} {
		seen := map[string]string{}
		seen256 := map[string]string{}
		for _, id := range roles {
			d := tokenTable[id]
			c := d.dark
			if mode == "light" {
				c = d.light
			}
			if prev, dup := seen[c.hex]; dup {
				t.Errorf("%s: %s and %s share %s", mode, prev, d.name, c.hex)
			}
			if prev, dup := seen256[c.x256]; dup {
				t.Errorf("%s: %s and %s share 256-colour %s", mode, prev, d.name, c.x256)
			}
			seen[c.hex], seen256[c.x256] = d.name, d.name
		}
	}
}

// The composer rule is quiet chrome (line.rule) like the card border; only the
// caret before the input carries the accent.
func TestChromeBorderMatchesCardBorder(t *testing.T) {
	restore := useProfile(t, termenv.ANSI256, true)
	defer restore()

	rule := kbrainPromptFrame().GetBorderTopForeground()
	card := bannerFrame.GetBorderTopForeground()
	if got, want := fmt.Sprint(rule), fmt.Sprint(card); got != want {
		t.Errorf("composer rule %s and card border %s should share line.rule", got, want)
	}
	if got, want := fmt.Sprint(rule), fmt.Sprint(tokColor(tokLineRule)); got != want {
		t.Errorf("composer rule colour = %s, want line.rule %s", got, want)
	}
}

// Every chrome style resolves through the token table to one styled cell.
func TestChromeStylesRenderColoured(t *testing.T) {
	restore := useProfile(t, termenv.ANSI256, true)
	defer restore()

	for name, style := range map[string]lipgloss.Style{
		"accentStyle": accentStyle, "brandStyle": brandStyle, "chromeStyle": chromeStyle,
		"dimStyle": dimStyle, "errStyle": errStyle, "warnStyle": warnStyle,
		"growStyle": growStyle, "toolStyle": toolStyle, "youStyle": youStyle,
		"botStyle": botStyle, "metaStyle": metaStyle, "shortcutStyle": shortcutStyle,
		"chromeRuleStyle": chromeRuleStyle, "statusTitleStyle": statusTitleStyle,
		"textHintStyle": textHintStyle, "runningStyle": runningStyle, "okStyle": okStyle,
	} {
		out := style.Render("x")
		if out == "x" || ansi.Strip(out) != "x" {
			t.Errorf("%s rendered %q: expected exactly one styled cell", name, out)
		}
		if !strings.Contains(out, "38;5;") {
			t.Errorf("%s did not resolve to a 256-colour foreground: %q", name, out)
		}
	}
}

// Mono mode drops every colour but keeps the attributes that carry meaning:
// muted text dims, the accent and errors go bold, the selection reverses.
func TestMonoModeKeepsAttributesOnly(t *testing.T) {
	restore := useProfile(t, termenv.ANSI256, true)
	defer restore()
	setMonoMode(true, false)
	for name, c := range map[string]struct {
		style lipgloss.Style
		sgr   string
	}{
		"muted":    {textMutedStyle, "2"},
		"accent":   {youStyle, "1"},
		"error":    {errorTitleStyle, "1"},
		"selected": {optionSelStyle, "7"},
		"always":   {modeChipAlways, "7"},
	} {
		out := c.style.Render("x")
		if strings.Contains(out, "38;") || strings.Contains(out, "48;") {
			t.Errorf("%s: mono must not colour: %q", name, out)
		}
		if !regexp.MustCompile(`\x1b\[([0-9;]*;)?` + c.sgr + `(;[0-9;]*)?m`).MatchString(out) {
			t.Errorf("%s: mono should keep SGR %s: %q", name, c.sgr, out)
		}
	}
	if colorsActive() || backgroundsActive() {
		t.Error("mono should report colours and backgrounds off")
	}
}

func TestWantMono(t *testing.T) {
	env := func(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }
	for _, tc := range []struct {
		env   map[string]string
		theme string
		want  bool
	}{
		{nil, "", false},
		{map[string]string{"NO_COLOR": "1"}, "", true},
		{map[string]string{"NO_COLOR": ""}, "", false},
		{map[string]string{"K_BRAIN_THEME": "mono"}, "", true},
		{nil, "mono", true},
		{nil, "dark", false},
	} {
		if got := wantMono(env(tc.env), tc.theme); got != tc.want {
			t.Errorf("wantMono(%v, %q) = %v, want %v", tc.env, tc.theme, got, tc.want)
		}
	}
}

func TestWantASCII(t *testing.T) {
	env := func(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }
	for _, tc := range []struct {
		env  map[string]string
		want bool
	}{
		{nil, false},
		{map[string]string{"K_BRAIN_ASCII": "1"}, true},
		{map[string]string{"K_BRAIN_ASCII": "0", "TERM": "linux"}, false},
		{map[string]string{"TERM": "linux"}, true},
		{map[string]string{"TERM": "xterm-256color", "LANG": "en_US.UTF-8"}, false},
		{map[string]string{"LANG": "en_US.ISO-8859-1"}, true},
		{map[string]string{"LC_ALL": "zh_CN.utf8", "LANG": "C"}, false},
		{map[string]string{"LANG": "C"}, false},
	} {
		if got := wantASCII(env(tc.env)); got != tc.want {
			t.Errorf("wantASCII(%v) = %v, want %v", tc.env, got, tc.want)
		}
	}
}

// Every glyph in either set is one cell wide, so switching sets never moves a
// column; the ASCII set is pure 7-bit.
func TestGlyphSetsAreOneCellAndASCIIIsSevenBit(t *testing.T) {
	check := func(set string, g glyphSet, sevenBit bool) {
		fields := map[string]string{
			"User": g.User, "Assistant": g.Assistant, "ToolQueued": g.ToolQueued, "ToolRun": g.ToolRun,
			"ToolDone": g.ToolDone, "OK": g.OK, "Fail": g.Fail, "Result": g.Result, "Rail": g.Rail,
			"Band": g.Band, "Notice": g.Notice, "Warn": g.Warn, "Effort": g.Effort, "Tokens": g.Tokens,
			"BarFull": g.BarFull, "BarEmpty": g.BarEmpty, "Minus": g.Minus,
		}
		for i, f := range g.Spinner {
			fields[fmt.Sprintf("Spinner[%d]", i)] = f
		}
		for name, v := range fields {
			if w := ansi.StringWidth(v); w != 1 {
				t.Errorf("%s.%s %q is %d cells wide", set, name, v, w)
			}
			if sevenBit {
				for _, r := range v {
					if r > 0x7e {
						t.Errorf("%s.%s %q is not 7-bit", set, name, v)
					}
				}
			}
		}
	}
	check("unicode", unicodeGlyphs, false)
	check("ascii", asciiGlyphs, true)
}

// Colours come from theme.go alone: a raw colour anywhere else bypasses the
// tokens and every fallback with them.
func TestNoRawColoursOutsideTheme(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	banned := regexp.MustCompile(`lipgloss\.Color\(|AdaptiveColor\{`)
	for _, f := range files {
		if f == "theme.go" || strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			if banned.MatchString(line) {
				t.Errorf("%s:%d uses a raw colour; add a token in theme.go instead: %s", f, i+1, strings.TrimSpace(line))
			}
		}
	}
}
