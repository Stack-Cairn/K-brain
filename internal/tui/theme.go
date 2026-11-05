package tui

import (
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"golang.org/x/term"
)

// The Krypton theme. Every colour the TUI paints with is a named token in this
// file; styles.go turns tokens into lipgloss styles and nothing else names a
// raw colour (theme_lint_test.go enforces that).
//
// Principles the token set encodes:
//   - the accent teal marks only what you can act on: the caret, the selected
//     option, focus;
//   - body text keeps the terminal's default foreground;
//   - every state carries a glyph as well as a colour, so it still reads in
//     16-colour, no-colour and ASCII mode;
//   - bold is for names and titles, dim for metadata, italic only for thinking,
//     underline only for links, reverse only for selection without colour.

// themeColor carries one palette entry across all three colour profiles.
// Distance-based downsampling collapses low-chroma colours to plain grey and
// loses the meaning they carry, so the 256- and 16-colour fallbacks stay
// hand-picked rather than computed. An empty ansi value means "no colour on a
// 16-colour terminal" and is only allowed for surfaces: ANSI 0 and 15 usually
// equal the terminal's own background, so a tint there would vanish or glare.
type themeColor struct {
	hex  string
	x256 string
	ansi string
}

func (c themeColor) complete() lipgloss.CompleteColor {
	return lipgloss.CompleteColor{TrueColor: c.hex, ANSI256: c.x256, ANSI: c.ansi}
}

// monoAttr is what a token turns into when there is no colour at all
// (NO_COLOR, K_BRAIN_THEME=mono): structure moves into text attributes.
type monoAttr uint8

const (
	monoPlain monoAttr = 0
	monoDim   monoAttr = 1 << iota
	monoBold
	monoItalic
	monoUnderline
	monoReverse
)

// tokenID names one style token. The order is the token table's order.
type tokenID int

const (
	tokTextPrimary tokenID = iota
	tokTextSecondary
	tokTextMuted
	tokTextHint
	tokSurfaceBand
	tokSurfaceRaised
	tokLineRule
	tokAccent
	tokRoleAssistant
	tokStateRunning
	tokStateOK
	tokStateWarn
	tokStateError
	tokDiffAddBG
	tokDiffDelBG
	tokSynKeyword
	tokSynString
	tokSynNumber
	tokSynFunc
	tokSynType
	tokSynComment
	tokChipInk
	tokLogoStem
	tokLogoUpper
	tokLogoLower
	tokenCount
)

// tokenDef is one row of the token table.
type tokenDef struct {
	name        string
	dark, light themeColor
	// termDefault tokens never carry a colour: body text keeps whatever the
	// terminal's own foreground is.
	termDefault bool
	// surface tokens are backgrounds; they drop out on 16-colour terminals.
	surface bool
	mono    monoAttr
}

var tokenTable = [tokenCount]tokenDef{
	tokTextPrimary:   {name: "text.primary", termDefault: true},
	tokTextSecondary: {name: "text.secondary", dark: themeColor{"#c8ccd4", "252", "7"}, light: themeColor{"#3a3f46", "238", "0"}},
	tokTextMuted:     {name: "text.muted", dark: themeColor{"#868b95", "245", "8"}, light: themeColor{"#686e76", "242", "8"}, mono: monoDim},
	tokTextHint:      {name: "text.hint", dark: themeColor{"#9aa0aa", "247", "7"}, light: themeColor{"#5c6269", "241", "8"}, mono: monoDim},
	tokSurfaceBand:   {name: "surface.band", dark: themeColor{"#1e222a", "235", ""}, light: themeColor{"#f3f5f7", "255", ""}, surface: true},
	tokSurfaceRaised: {name: "surface.raised", dark: themeColor{"#262b35", "236", ""}, light: themeColor{"#e8ecf0", "254", ""}, surface: true, mono: monoReverse},
	tokLineRule:      {name: "line.rule", dark: themeColor{"#3a4048", "238", "8"}, light: themeColor{"#d5dade", "252", "7"}},
	tokAccent:        {name: "accent", dark: themeColor{"#2fbfa0", "79", "14"}, light: themeColor{"#157f68", "29", "6"}, mono: monoBold},
	tokRoleAssistant: {name: "role.assistant", dark: themeColor{"#b18cff", "141", "13"}, light: themeColor{"#7d3f9e", "90", "5"}, mono: monoBold},
	tokStateRunning:  {name: "state.running", dark: themeColor{"#56b6c2", "80", "6"}, light: themeColor{"#2f5fa8", "25", "4"}},
	tokStateOK:       {name: "state.ok", dark: themeColor{"#74b87a", "108", "10"}, light: themeColor{"#2f7d3a", "28", "2"}},
	tokStateWarn:     {name: "state.warn", dark: themeColor{"#d9a441", "179", "11"}, light: themeColor{"#a8741a", "136", "3"}, mono: monoBold},
	tokStateError:    {name: "state.error", dark: themeColor{"#e0696a", "167", "9"}, light: themeColor{"#b94b4d", "131", "1"}, mono: monoBold},
	tokDiffAddBG:     {name: "diff.add.bg", dark: themeColor{"#16301f", "22", ""}, light: themeColor{"#e3f6e7", "194", ""}, surface: true},
	tokDiffDelBG:     {name: "diff.del.bg", dark: themeColor{"#3a1a1d", "52", ""}, light: themeColor{"#fbe5e5", "224", ""}, surface: true},
	tokSynKeyword:    {name: "syn.keyword", dark: themeColor{"#c49bff", "141", "13"}, light: themeColor{"#7d3f9e", "90", "5"}},
	tokSynString:     {name: "syn.string", dark: themeColor{"#8cc590", "114", "10"}, light: themeColor{"#2f7d3a", "28", "2"}},
	tokSynNumber:     {name: "syn.number", dark: themeColor{"#e0a85a", "179", "11"}, light: themeColor{"#a8741a", "136", "3"}},
	tokSynFunc:       {name: "syn.func", dark: themeColor{"#6cc4d0", "80", "14"}, light: themeColor{"#1f6f8b", "31", "6"}},
	tokSynType:       {name: "syn.type", dark: themeColor{"#7fb4ff", "111", "12"}, light: themeColor{"#2f5fa8", "25", "4"}},
	tokSynComment:    {name: "syn.comment", dark: themeColor{"#6c727c", "243", "8"}, light: themeColor{"#7a8089", "244", "8"}, mono: monoItalic},
	// chip.ink is the text on a filled mode chip.
	tokChipInk: {name: "chip.ink", dark: themeColor{"#14171c", "234", "0"}, light: themeColor{"#ffffff", "231", "15"}},
	// The K mark keeps the product logo's own gradient rather than the UI
	// accent: the stem is blue, the upper arm cyan, the lower arm violet. A logo
	// is the one place a second colour family earns its keep.
	tokLogoStem:  {name: "logo.stem", dark: themeColor{"#2f6df6", "33", "4"}, light: themeColor{"#2f6df6", "33", "4"}, mono: monoBold},
	tokLogoUpper: {name: "logo.upper", dark: themeColor{"#3ad4ff", "45", "14"}, light: themeColor{"#1691bd", "31", "6"}, mono: monoBold},
	tokLogoLower: {name: "logo.lower", dark: themeColor{"#8b5cf6", "99", "5"}, light: themeColor{"#7c4ddf", "98", "5"}, mono: monoBold},
}

// monoMode drops every colour; styles fall back to the tokens' mono attributes.
// It is set from NO_COLOR, K_BRAIN_THEME=mono or the "mono" theme setting.
var monoMode bool

// tokColor resolves a token for the active background and colour mode.
func tokColor(id tokenID) lipgloss.TerminalColor {
	d := tokenTable[id]
	if monoMode || d.termDefault {
		return lipgloss.NoColor{}
	}
	return lipgloss.CompleteAdaptiveColor{Light: d.light.complete(), Dark: d.dark.complete()}
}

// tokString is a token as a plain colour string for the active profile and
// background, for renderers (glamour) that take strings rather than lipgloss
// colours. It is "" whenever the token should not paint.
func tokString(id tokenID) string {
	d := tokenTable[id]
	if monoMode || d.termDefault {
		return ""
	}
	c := d.dark
	if !lipgloss.HasDarkBackground() {
		c = d.light
	}
	switch lipgloss.ColorProfile() {
	case termenv.TrueColor:
		return c.hex
	case termenv.ANSI256:
		return c.x256
	case termenv.ANSI:
		return c.ansi
	}
	return ""
}

// tokHex is a token's truecolor value for the active background, for chroma,
// which only accepts hex and does its own downsampling.
func tokHex(id tokenID) string {
	d := tokenTable[id]
	if monoMode || d.termDefault {
		return ""
	}
	if !lipgloss.HasDarkBackground() {
		return d.light.hex
	}
	return d.dark.hex
}

// colorsActive reports whether the terminal shows colour at all right now.
func colorsActive() bool {
	return !monoMode && lipgloss.ColorProfile() != termenv.Ascii
}

// backgroundsActive reports whether surface tints show: they need at least a
// 256-colour terminal (see themeColor).
func backgroundsActive() bool {
	if monoMode {
		return false
	}
	p := lipgloss.ColorProfile()
	return p == termenv.TrueColor || p == termenv.ANSI256
}

// Legacy colour names. styles.go reassigns them whenever the colour mode
// changes, so they always agree with tokColor().
var (
	accentColor, mutedColor, faintColor, subtleColor   lipgloss.TerminalColor
	successColor, warnColor, errColor                  lipgloss.TerminalColor
	infoColor, secondaryColor, borderColor             lipgloss.TerminalColor
	panelBGColor, selectionBGColor                     lipgloss.TerminalColor
	logoStemColor, logoUpperColor, logoLowerColor      lipgloss.TerminalColor
	chipInkColor, diffAddBGColor, diffDelBGColor       lipgloss.TerminalColor
	runningColor, assistantColor, ruleColor, hintColor lipgloss.TerminalColor
)

func assignColors() {
	accentColor, mutedColor, faintColor, subtleColor = tokColor(tokAccent), tokColor(tokTextSecondary), tokColor(tokTextMuted), tokColor(tokTextHint)
	successColor, warnColor, errColor = tokColor(tokStateOK), tokColor(tokStateWarn), tokColor(tokStateError)
	infoColor, secondaryColor, borderColor = tokColor(tokStateRunning), tokColor(tokRoleAssistant), tokColor(tokLineRule)
	panelBGColor, selectionBGColor = tokColor(tokSurfaceBand), tokColor(tokSurfaceRaised)
	logoStemColor, logoUpperColor, logoLowerColor = tokColor(tokLogoStem), tokColor(tokLogoUpper), tokColor(tokLogoLower)
	chipInkColor, diffAddBGColor, diffDelBGColor = tokColor(tokChipInk), tokColor(tokDiffAddBG), tokColor(tokDiffDelBG)
	runningColor, assistantColor, ruleColor, hintColor = tokColor(tokStateRunning), tokColor(tokRoleAssistant), tokColor(tokLineRule), tokColor(tokTextHint)
}

// wantMono reports whether the environment or the saved theme asks for no
// colour. NO_COLOR follows no-color.org: any non-empty value counts.
func wantMono(getenv func(string) string, theme string) bool {
	return getenv("NO_COLOR") != "" || strings.EqualFold(getenv("K_BRAIN_THEME"), "mono") || theme == "mono"
}

// setMonoMode switches the colour mode and rebuilds every style. Without
// colour the styles still use bold, dim, italic and reverse, so when termenv
// has dropped to its no-escape profile only because of NO_COLOR, an
// interactive terminal is moved back to plain ANSI so those attributes reach it.
func setMonoMode(on bool, tty bool) {
	monoMode = on
	switch {
	case on && tty && lipgloss.ColorProfile() == termenv.Ascii:
		lipgloss.SetColorProfile(termenv.ANSI)
		monoRaised = true
	case !on && monoRaised:
		lipgloss.SetColorProfile(termenv.Ascii)
		monoRaised = false
	}
	buildStyles()
	invalidateMDRenderer()
}

// monoRaised records that setMonoMode moved the profile up from Ascii, so
// leaving mono puts it back.
var monoRaised bool

// stdoutIsTTY reports whether attributes can reach a real terminal.
func stdoutIsTTY() bool {
	return term.IsTerminal(int(os.Stdout.Fd())) && !strings.EqualFold(os.Getenv("TERM"), "dumb")
}

// pickTheme applies a theme chosen from the settings panel: auto, light and
// dark go through setTheme; mono keeps the detected background and drops
// colour, and is saved like any other theme.
func (m *model) pickTheme(theme string) {
	defer m.restyleTranscript()
	if theme != "mono" {
		m.setTheme(theme)
		setMonoMode(wantMono(os.Getenv, m.cfg.Theme), stdoutIsTTY())
		return
	}
	m.themeHow = m.applyTheme(theme)
	m.cfg.Theme = theme
	if m.cfgExtra == nil {
		m.cfgExtra = map[string]string{}
	}
	m.cfgExtra["theme"] = theme
	if err := m.cfg.Save(); err != nil {
		m.append(errStyle.Render("config save failed: " + err.Error()))
	}
	setMonoMode(true, stdoutIsTTY())
}

// restyleTranscript re-renders every block after a palette change; rendered
// blocks are cached per width, and the cache would keep the old colours.
func (m *model) restyleTranscript() {
	for i := range m.blocks {
		m.blocks[i].stale = true
	}
	m.refreshVP()
}

// applyColorEnv settles the colour mode and glyph set before anything is drawn.
func (m *model) applyColorEnv() {
	theme := ""
	if m.cfg != nil {
		theme = m.cfg.Theme
	}
	setASCIIGlyphs(wantASCII(os.Getenv))
	setMonoMode(wantMono(os.Getenv, theme), stdoutIsTTY())
}
