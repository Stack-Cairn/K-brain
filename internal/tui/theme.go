package tui

import "github.com/charmbracelet/lipgloss"

// themeColor carries one palette entry across all three colour profiles.
// Distance-based downsampling collapses low-chroma colours to plain grey and
// loses the meaning they carry, so the 256- and 16-colour fallbacks stay
// hand-picked rather than computed.
type themeColor struct {
	hex  string
	x256 string
	ansi string
}

func (c themeColor) complete() lipgloss.CompleteColor {
	return lipgloss.CompleteColor{TrueColor: c.hex, ANSI256: c.x256, ANSI: c.ansi}
}

// themePalette is the set of semantic roles the chrome paints with. Nothing outside
// this file should name a raw colour.
type themePalette struct {
	// accent is K-brain's 氪光青 — krypton teal. It is spent only on the one
	// actionable thing in a given surface, never on decoration.
	accent themeColor
	// muted is body and value text; faint is labels and de-emphasised chrome;
	// subtle sits between them for keyboard hints.
	muted  themeColor
	faint  themeColor
	subtle themeColor

	success themeColor
	warn    themeColor
	err     themeColor
	// info and secondary tag the two conversation voices.
	info      themeColor
	secondary themeColor

	border      themeColor
	panelBG     themeColor
	selectionBG themeColor
}

var darkPalette = themePalette{
	accent:      themeColor{"#2fbfa0", "79", "14"},
	muted:       themeColor{"#c8ccd4", "252", "15"},
	faint:       themeColor{"#868b95", "245", "8"},
	subtle:      themeColor{"#9aa0aa", "247", "7"},
	success:     themeColor{"#74b87a", "108", "10"},
	warn:        themeColor{"#d9a441", "179", "11"},
	err:         themeColor{"#e0696a", "167", "9"},
	info:        themeColor{"#56b6c2", "80", "6"},
	secondary:   themeColor{"#b18cff", "141", "13"},
	border:      themeColor{"#3a4048", "238", "8"},
	panelBG:     themeColor{"#222631", "235", "0"},
	selectionBG: themeColor{"#2a2f3a", "236", "0"},
}

var lightPalette = themePalette{
	accent:      themeColor{"#157f68", "29", "6"},
	muted:       themeColor{"#3a3f46", "238", "0"},
	faint:       themeColor{"#6f757d", "243", "8"},
	subtle:      themeColor{"#5c6269", "241", "8"},
	success:     themeColor{"#2f7d3a", "28", "2"},
	warn:        themeColor{"#a8741a", "136", "3"},
	err:         themeColor{"#b94b4d", "131", "1"},
	info:        themeColor{"#2f5fa8", "25", "4"},
	secondary:   themeColor{"#7d3f9e", "90", "5"},
	border:      themeColor{"#d5dade", "252", "7"},
	panelBG:     themeColor{"#f4f6f8", "255", "15"},
	selectionBG: themeColor{"#e8ecf0", "254", "15"},
}

// themed resolves one role for the active background. Every chrome style is
// built from this, so a palette edit is the only place a colour changes.
func themed(role func(themePalette) themeColor) lipgloss.CompleteAdaptiveColor {
	return lipgloss.CompleteAdaptiveColor{
		Light: role(lightPalette).complete(),
		Dark:  role(darkPalette).complete(),
	}
}

// The K mark keeps the product logo's own gradient rather than the UI accent:
// the stem is blue, the upper arm cyan, the lower arm violet. A logo is the one
// place a second colour family earns its keep.
var (
	logoStemColor  = themed(func(themePalette) themeColor { return themeColor{"#2f6df6", "33", "4"} })
	logoUpperColor = themed(func(themePalette) themeColor { return themeColor{"#3ad4ff", "45", "14"} })
	logoLowerColor = themed(func(themePalette) themeColor { return themeColor{"#8b5cf6", "99", "5"} })
)

var (
	accentColor      = themed(func(p themePalette) themeColor { return p.accent })
	mutedColor       = themed(func(p themePalette) themeColor { return p.muted })
	faintColor       = themed(func(p themePalette) themeColor { return p.faint })
	subtleColor      = themed(func(p themePalette) themeColor { return p.subtle })
	successColor     = themed(func(p themePalette) themeColor { return p.success })
	warnColor        = themed(func(p themePalette) themeColor { return p.warn })
	errColor         = themed(func(p themePalette) themeColor { return p.err })
	infoColor        = themed(func(p themePalette) themeColor { return p.info })
	secondaryColor   = themed(func(p themePalette) themeColor { return p.secondary })
	borderColor      = themed(func(p themePalette) themeColor { return p.border })
	panelBGColor     = themed(func(p themePalette) themeColor { return p.panelBG })
	selectionBGColor = themed(func(p themePalette) themeColor { return p.selectionBG })
)
