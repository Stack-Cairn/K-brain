package tui

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

func themeRoles() map[string]func(themePalette) themeColor {
	return map[string]func(themePalette) themeColor{
		"accent":      func(p themePalette) themeColor { return p.accent },
		"muted":       func(p themePalette) themeColor { return p.muted },
		"faint":       func(p themePalette) themeColor { return p.faint },
		"subtle":      func(p themePalette) themeColor { return p.subtle },
		"success":     func(p themePalette) themeColor { return p.success },
		"warn":        func(p themePalette) themeColor { return p.warn },
		"err":         func(p themePalette) themeColor { return p.err },
		"info":        func(p themePalette) themeColor { return p.info },
		"secondary":   func(p themePalette) themeColor { return p.secondary },
		"border":      func(p themePalette) themeColor { return p.border },
		"panelBG":     func(p themePalette) themeColor { return p.panelBG },
		"selectionBG": func(p themePalette) themeColor { return p.selectionBG },
	}
}

// Every role needs all three profiles filled in: lipgloss picks one by profile
// and an empty string silently renders uncoloured.
func TestPaletteCoversEveryProfile(t *testing.T) {
	for _, tc := range []struct {
		mode string
		p    themePalette
	}{{"dark", darkPalette}, {"light", lightPalette}} {
		for name, role := range themeRoles() {
			c := role(tc.p)
			if !strings.HasPrefix(c.hex, "#") || len(c.hex) != 7 {
				t.Errorf("%s/%s: truecolor %q is not a 6-digit hex", tc.mode, name, c.hex)
			}
			for label, v := range map[string]string{"ANSI256": c.x256, "ANSI": c.ansi} {
				n, err := strconv.Atoi(v)
				if err != nil {
					t.Errorf("%s/%s: %s fallback %q is not a number", tc.mode, name, label, v)
					continue
				}
				limit := 255
				if label == "ANSI" {
					limit = 15
				}
				if n < 0 || n > limit {
					t.Errorf("%s/%s: %s fallback %d out of range 0..%d", tc.mode, name, label, n, limit)
				}
			}
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
	defer func() {
		lipgloss.SetColorProfile(termenv.Ascii)
		lipgloss.SetHasDarkBackground(true)
	}()
	for _, tc := range []struct {
		profile termenv.Profile
		want    func(themeColor) string
	}{
		{termenv.ANSI256, func(c themeColor) string { return "38;5;" + c.x256 }},
		{termenv.ANSI, func(c themeColor) string { return ansi16SGR(c.ansi) }},
	} {
		lipgloss.SetColorProfile(tc.profile)
		lipgloss.SetHasDarkBackground(true)
		got := lipgloss.NewStyle().Foreground(accentColor).Render("x")
		if want := tc.want(darkPalette.accent); !strings.Contains(got, want) {
			t.Errorf("profile %v: accent rendered %q, want a %q sequence", tc.profile, got, want)
		}
	}
}

func ansiHexLuminance(hex string) float64 {
	var r, g, b int
	if _, err := fmt.Sscanf(hex, "#%02x%02x%02x", &r, &g, &b); err != nil {
		return -1
	}
	return (0.2126*float64(r) + 0.7152*float64(g) + 0.0722*float64(b)) / 255
}

// The accent is the one colour the eye is meant to land on, so it has to hold up
// against both backgrounds — a teal that washes out on white is useless.
func TestAccentLegibleOnBothBackgrounds(t *testing.T) {
	for _, tc := range []struct {
		mode  string
		hex   string
		bgLum float64
	}{
		{"dark", darkPalette.accent.hex, 0.05},
		{"light", lightPalette.accent.hex, 1.0},
	} {
		lum := ansiHexLuminance(tc.hex)
		if lum < 0 {
			t.Fatalf("%s accent %q is unparseable", tc.mode, tc.hex)
		}
		if gap := max(lum-tc.bgLum, tc.bgLum-lum); gap < 0.28 {
			t.Errorf("%s accent %s luminance gap %.2f is too low to read", tc.mode, tc.hex, gap)
		}
	}
}

// Errors and warnings must not collapse into the accent, or "something is wrong"
// and "here is the action" become indistinguishable.
func TestSemanticColoursAreDistinct(t *testing.T) {
	for _, tc := range []struct {
		mode string
		p    themePalette
	}{{"dark", darkPalette}, {"light", lightPalette}} {
		seen := map[string]string{}
		for _, role := range []struct {
			name string
			c    themeColor
		}{
			{"accent", tc.p.accent}, {"success", tc.p.success},
			{"warn", tc.p.warn}, {"err", tc.p.err},
			{"info", tc.p.info}, {"secondary", tc.p.secondary},
		} {
			if prev, dup := seen[role.c.hex]; dup {
				t.Errorf("%s: %s and %s share %s", tc.mode, prev, role.name, role.c.hex)
			}
			seen[role.c.hex] = role.name
			if role.c.x256 == tc.p.accent.x256 && role.name != "accent" {
				t.Errorf("%s: %s collides with the accent on 256-colour terminals", tc.mode, role.name)
			}
		}
	}
}

// The composer rules and the card border share one colour so the chrome reads as
// a single system.
func TestChromeBorderMatchesCardBorder(t *testing.T) {
	lipgloss.SetColorProfile(termenv.ANSI256)
	defer lipgloss.SetColorProfile(termenv.Ascii)

	rule := kbrainPromptFrame().GetBorderTopForeground()
	card := bannerFrame.GetBorderTopForeground()
	if fmt.Sprint(rule) == fmt.Sprint(card) {
		t.Fatal("the composer rule and the card border are expected to differ: the rule is the accent, the card is the quiet border")
	}
	if got, want := fmt.Sprint(rule), fmt.Sprint(accentColor); got != want {
		t.Errorf("composer rule colour = %s, want the accent %s", got, want)
	}
	if got, want := fmt.Sprint(card), fmt.Sprint(borderColor); got != want {
		t.Errorf("card border colour = %s, want the quiet border %s", got, want)
	}
}

// A palette swap must not leak raw colours into the chrome: every style in the
// var block should resolve through the palette.
func TestChromeStylesRenderColoured(t *testing.T) {
	lipgloss.SetColorProfile(termenv.ANSI256)
	defer lipgloss.SetColorProfile(termenv.Ascii)
	lipgloss.SetHasDarkBackground(true)

	for name, style := range map[string]lipgloss.Style{
		"accentStyle": accentStyle, "brandStyle": brandStyle, "chromeStyle": chromeStyle,
		"dimStyle": dimStyle, "errStyle": errStyle, "warnStyle": warnStyle,
		"growStyle": growStyle, "toolStyle": toolStyle, "youStyle": youStyle,
		"botStyle": botStyle, "metaStyle": metaStyle, "shortcutStyle": shortcutStyle,
		"chromeRuleStyle": chromeRuleStyle, "statusTitleStyle": statusTitleStyle,
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
