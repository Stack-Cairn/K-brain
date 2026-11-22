package tui

import (
	"slices"
	"strings"
)

// Named themes. A theme is a family of up to two variants, one for dark and
// one for light terminals; a family with both follows the terminal's
// background (and switches when it changes), a family with one variant
// always uses it. Every variant is a full palette: a colour per token at
// every depth. Built-in families are written as overrides of the default
// theme's variant of the same polarity, so a token a family does not mention
// (the logo gradient, for one) keeps the default colour.
//
// theme_palette_test.go holds every variant to the same rules as the default:
// WCAG contrast for text and signals on a typical background at truecolor
// and 256 colours, distinct semantic colours, and 16-colour values that do
// not land on the terminal's own background slot.

// themePalette is one variant: a colour per token. text.primary stays empty.
type themePalette [tokenCount]themeColor

// themeOverrides is a sparse palette, applied on top of a base variant.
type themeOverrides map[tokenID]themeColor

// themeDef is a named theme family.
type themeDef struct {
	name string
	desc string
	// dark and light are the variants; nil when the family has none.
	dark, light *themePalette
	// highContrast themes are held to AAA-level contrast in the tests.
	highContrast bool
	// source is the file a custom theme came from; "" for built-ins.
	source string
	// warnings are non-fatal findings from loading a custom theme.
	warnings []string
}

// polarity names the variant the family is limited to: "dark", "light", or
// "" when it has both and follows the terminal.
func (t *themeDef) polarity() string {
	switch {
	case t.dark != nil && t.light == nil:
		return "dark"
	case t.light != nil && t.dark == nil:
		return "light"
	}
	return ""
}

// variant returns the palette for a background, falling back to the
// family's only variant.
func (t *themeDef) variant(light bool) *themePalette {
	if light && t.light != nil || t.dark == nil {
		return t.light
	}
	return t.dark
}

// defaultVariant is the default theme's palette for one polarity.
func defaultVariant(light bool) *themePalette {
	var p themePalette
	for id, d := range defaultTokens {
		if light {
			p[id] = d.light
		} else {
			p[id] = d.dark
		}
	}
	return &p
}

// overlay returns base with the overrides applied.
func overlay(base *themePalette, o themeOverrides) *themePalette {
	p := *base
	for id, c := range o {
		p[id] = c
	}
	return &p
}

// tc is shorthand for one palette entry in the built-in tables.
func tc(hex, x256, ansi string) themeColor { return themeColor{hex, x256, ansi} }

// defaultThemeName is the family behind "auto", "light" and "dark".
const defaultThemeName = "default"

var builtinThemes = []*themeDef{
	{
		name: defaultThemeName, desc: "teal accent, neutral greys",
		dark: defaultVariant(false), light: defaultVariant(true),
	},
	{
		name: "dusk", desc: "dark · amber accent on violet greys",
		dark: overlay(defaultVariant(false), themeOverrides{
			tokTextSecondary: tc("#d6cce2", "252", "7"),
			tokTextMuted:     tc("#968aa8", "103", "8"),
			tokTextHint:      tc("#a99dbb", "248", "7"),
			tokSurfaceBand:   tc("#241d30", "235", ""),
			tokSurfaceRaised: tc("#30273f", "236", ""),
			tokLineRule:      tc("#4a3f5c", "238", "8"),
			tokAccent:        tc("#f0a35e", "215", "3"),
			tokRoleAssistant: tc("#c99cf2", "183", "13"),
			tokStateRunning:  tc("#7fb8e0", "110", "12"),
			tokStateOK:       tc("#8fca8c", "114", "10"),
			tokStateWarn:     tc("#e9c46a", "185", "11"),
			tokStateError:    tc("#f27f8c", "210", "9"),
			tokDiffAddBG:     tc("#1d3024", "22", ""),
			tokDiffDelBG:     tc("#3d1f2a", "52", ""),
			tokSynKeyword:    tc("#d49df5", "183", "13"),
			tokSynString:     tc("#a9d18e", "150", "10"),
			tokSynNumber:     tc("#f2b26b", "215", "3"),
			tokSynFunc:       tc("#86c5e8", "116", "14"),
			tokSynType:       tc("#f5a3c7", "218", "5"),
			tokSynComment:    tc("#8a7f9c", "103", "8"),
			tokChipInk:       tc("#1a1522", "234", "0"),
		}),
	},
	{
		name: "harbor", desc: "sky-blue accent on slate, dark and light",
		dark: overlay(defaultVariant(false), themeOverrides{
			tokTextSecondary: tc("#c6d0dc", "252", "7"),
			tokTextMuted:     tc("#8593a5", "103", "8"),
			tokTextHint:      tc("#9aa8ba", "248", "7"),
			tokSurfaceBand:   tc("#18212c", "235", ""),
			tokSurfaceRaised: tc("#213042", "236", ""),
			tokLineRule:      tc("#34465a", "238", "8"),
			tokAccent:        tc("#4fb3ff", "75", "12"),
			tokRoleAssistant: tc("#a3a8ff", "147", "13"),
			tokStateRunning:  tc("#5fd0c8", "80", "6"),
			tokStateOK:       tc("#6cc38c", "72", "10"),
			tokStateWarn:     tc("#e5b84d", "179", "11"),
			tokStateError:    tc("#ff7b72", "209", "9"),
			tokDiffAddBG:     tc("#133024", "22", ""),
			tokDiffDelBG:     tc("#3a1b22", "52", ""),
			tokSynKeyword:    tc("#a3a8ff", "147", "13"),
			tokSynString:     tc("#9fd49b", "150", "10"),
			tokSynNumber:     tc("#f2b56b", "215", "11"),
			tokSynFunc:       tc("#5fd0e8", "80", "14"),
			tokSynType:       tc("#7cc4ff", "117", "12"),
			tokSynComment:    tc("#728196", "244", "8"),
			tokChipInk:       tc("#0d1520", "233", "0"),
		}),
		light: overlay(defaultVariant(true), themeOverrides{
			tokTextSecondary: tc("#2d3a4a", "237", "0"),
			tokTextMuted:     tc("#5b6b7e", "60", "8"),
			tokTextHint:      tc("#4f5f72", "59", "8"),
			tokSurfaceBand:   tc("#eef4fa", "255", ""),
			tokSurfaceRaised: tc("#dde9f5", "189", ""),
			tokLineRule:      tc("#c9d6e3", "188", "7"),
			tokAccent:        tc("#0b6bb8", "25", "4"),
			tokRoleAssistant: tc("#5a4fc4", "62", "5"),
			tokStateRunning:  tc("#0f7a74", "30", "6"),
			tokStateOK:       tc("#2e7d4a", "29", "2"),
			tokStateWarn:     tc("#8f6400", "94", "3"),
			tokStateError:    tc("#c0392b", "160", "1"),
			tokDiffAddBG:     tc("#e2f4e8", "194", ""),
			tokDiffDelBG:     tc("#fbe4e2", "224", ""),
			tokSynKeyword:    tc("#5a4fc4", "62", "5"),
			tokSynString:     tc("#2e7d4a", "29", "2"),
			tokSynNumber:     tc("#9a5d00", "94", "3"),
			tokSynFunc:       tc("#0f6f8f", "24", "6"),
			tokSynType:       tc("#0b6bb8", "25", "4"),
			tokSynComment:    tc("#6b7889", "243", "8"),
			tokChipInk:       tc("#ffffff", "231", "15"),
		}),
	},
	{
		name: "paper", desc: "light · ink and rust on warm paper",
		light: overlay(defaultVariant(true), themeOverrides{
			tokTextSecondary: tc("#3f3a33", "237", "0"),
			tokTextMuted:     tc("#6e665b", "242", "8"),
			tokTextHint:      tc("#5f584e", "240", "8"),
			tokSurfaceBand:   tc("#f4efe4", "255", ""),
			tokSurfaceRaised: tc("#ebe3d3", "254", ""),
			tokLineRule:      tc("#d8cfbd", "252", "7"),
			tokAccent:        tc("#b0501a", "130", "1"),
			tokRoleAssistant: tc("#8a3f7a", "96", "5"),
			tokStateRunning:  tc("#2a6a8a", "24", "4"),
			tokStateOK:       tc("#4a7a28", "64", "2"),
			tokStateWarn:     tc("#8a6200", "94", "3"),
			tokStateError:    tc("#b3261e", "124", "9"),
			tokDiffAddBG:     tc("#e6f0d8", "194", ""),
			tokDiffDelBG:     tc("#f6e0da", "224", ""),
			tokSynKeyword:    tc("#8a3f7a", "96", "5"),
			tokSynString:     tc("#4a7a28", "64", "2"),
			tokSynNumber:     tc("#a0560f", "130", "3"),
			tokSynFunc:       tc("#2a6a8a", "24", "6"),
			tokSynType:       tc("#6b4fa0", "61", "4"),
			tokSynComment:    tc("#7d7466", "243", "8"),
			tokChipInk:       tc("#ffffff", "231", "15"),
		}),
	},
	{
		name: "contrast", desc: "high contrast, dark and light", highContrast: true,
		dark: overlay(defaultVariant(false), themeOverrides{
			tokTextSecondary: tc("#ececec", "255", "15"),
			tokTextMuted:     tc("#bdbdbd", "250", "7"),
			tokTextHint:      tc("#d0d0d0", "252", "7"),
			tokSurfaceBand:   tc("#0f0f0f", "233", ""),
			tokSurfaceRaised: tc("#3a3a3a", "237", ""),
			tokLineRule:      tc("#5a5a5a", "240", "8"),
			tokAccent:        tc("#00e5ff", "45", "14"),
			tokRoleAssistant: tc("#ff9cff", "219", "13"),
			tokStateRunning:  tc("#7ab8ff", "111", "12"),
			tokStateOK:       tc("#5cff7a", "84", "10"),
			tokStateWarn:     tc("#ffd75f", "221", "11"),
			tokStateError:    tc("#ff7070", "203", "9"),
			tokDiffAddBG:     tc("#003d1a", "22", ""),
			tokDiffDelBG:     tc("#4d0000", "52", ""),
			tokSynKeyword:    tc("#ff9cff", "219", "13"),
			tokSynString:     tc("#8affa0", "121", "10"),
			tokSynNumber:     tc("#ffd75f", "221", "11"),
			tokSynFunc:       tc("#66e8ff", "81", "14"),
			tokSynType:       tc("#a8c8ff", "153", "12"),
			tokSynComment:    tc("#b0b0b0", "145", "7"),
			tokChipInk:       tc("#000000", "16", "0"),
		}),
		light: overlay(defaultVariant(true), themeOverrides{
			tokTextSecondary: tc("#141414", "233", "0"),
			tokTextMuted:     tc("#3d3d3d", "237", "0"),
			tokTextHint:      tc("#2e2e2e", "236", "0"),
			tokSurfaceBand:   tc("#f0f0f0", "255", ""),
			tokSurfaceRaised: tc("#d4d4d4", "252", ""),
			tokLineRule:      tc("#a0a0a0", "247", "8"),
			tokAccent:        tc("#00577a", "24", "4"),
			tokRoleAssistant: tc("#6a0080", "54", "5"),
			tokStateRunning:  tc("#003c9e", "19", "4"),
			tokStateOK:       tc("#005c26", "22", "2"),
			tokStateWarn:     tc("#6b4400", "58", "3"),
			tokStateError:    tc("#a8001c", "124", "1"),
			tokDiffAddBG:     tc("#d7f5df", "194", ""),
			tokDiffDelBG:     tc("#ffd9d9", "224", ""),
			tokSynKeyword:    tc("#6a0080", "54", "5"),
			tokSynString:     tc("#005c26", "22", "2"),
			tokSynNumber:     tc("#6b4400", "58", "3"),
			tokSynFunc:       tc("#00566b", "23", "6"),
			tokSynType:       tc("#003c9e", "19", "4"),
			tokSynComment:    tc("#474747", "238", "8"),
			tokChipInk:       tc("#ffffff", "231", "15"),
		}),
	},
}

// customThemes are the user's theme files, loaded by loadCustomThemes.
var customThemes []*themeDef

// allThemes is every family, built-ins first.
func allThemes() []*themeDef {
	return append(slices.Clip(builtinThemes), customThemes...)
}

// lookupTheme finds a family by name, case-insensitively.
func lookupTheme(name string) *themeDef {
	for _, t := range allThemes() {
		if strings.EqualFold(t.name, name) {
			return t
		}
	}
	return nil
}

// activeTheme is the family tokenTable currently holds.
var activeTheme = builtinThemes[0]

// activateTheme loads a family's colours into the token table and rebuilds
// every style. A family with one variant uses it for both columns, so the
// adaptive colours resolve to it whatever lipgloss thinks the background is.
func activateTheme(t *themeDef) {
	if t == nil {
		t = builtinThemes[0]
	}
	activeTheme = t
	dark, light := t.variant(false), t.variant(true)
	for id := range tokenTable {
		tokenTable[id].dark, tokenTable[id].light = dark[id], light[id]
	}
	buildStyles()
	invalidateMDRenderer()
}

// themeChoice is a parsed theme setting.
type themeChoice struct {
	family string
	// pin is "light" or "dark" when the setting fixes the variant, "" when it
	// follows the terminal (or the family has only one variant).
	pin  string
	mono bool
}

// parseThemeSetting reads the theme setting: "" or "auto", "light", "dark",
// "mono", a family name, or a family name with ":light" or ":dark" to fix
// its variant. ok is false for a family that does not exist.
func parseThemeSetting(s string) (themeChoice, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "", "auto":
		return themeChoice{family: defaultThemeName}, true
	case "light", "dark":
		return themeChoice{family: defaultThemeName, pin: s}, true
	case "mono":
		return themeChoice{family: defaultThemeName, mono: true}, true
	}
	name, pin, hasPin := strings.Cut(s, ":")
	if hasPin && pin != "light" && pin != "dark" {
		return themeChoice{family: defaultThemeName}, false
	}
	t := lookupTheme(name)
	if t == nil {
		return themeChoice{family: defaultThemeName}, false
	}
	if p := t.polarity(); p != "" {
		pin = p
	}
	return themeChoice{family: t.name, pin: pin}, true
}

// themeChoices is what the theme picker and the settings row offer, in
// order: the default family's three modes, the other built-ins, the user's
// own themes, then mono.
func themeChoices() []string {
	out := []string{"auto", "light", "dark"}
	for _, t := range allThemes()[1:] {
		out = append(out, t.name)
	}
	return append(out, "mono")
}

// themeChoiceDesc is the one-line description the picker shows for a choice.
func themeChoiceDesc(name string) string {
	switch name {
	case "auto":
		return "default palette, follows the terminal background"
	case "light":
		return "default palette, light variant"
	case "dark":
		return "default palette, dark variant"
	case "mono":
		return "no colour: bold, dim and reverse only"
	}
	t := lookupTheme(name)
	if t == nil {
		return ""
	}
	d := t.desc
	if t.source != "" {
		d = strings.TrimSpace("custom · " + d)
	}
	return d
}

// themeLabel names the active theme: the polarity ("dark", "light", "auto")
// for the default family, prefixed by the family's name for any other.
func themeLabel() string {
	if activeTheme == nil || activeTheme.name == defaultThemeName {
		return CurrentTheme()
	}
	return activeTheme.name + " · " + CurrentTheme()
}
