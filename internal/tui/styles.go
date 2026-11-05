package tui

import "github.com/charmbracelet/lipgloss"

// Every lipgloss style the TUI paints with, built from the theme tokens. The
// older names (dimStyle, accentStyle, …) stay as aliases of the token styles so
// code that predates the token layer picks the theme up unchanged.
//
// buildStyles reassigns all of them whenever the colour mode or glyph set
// changes, so nothing may capture a style in a package-level initialiser.
var (
	// Token styles.
	textSecondaryStyle lipgloss.Style
	textMutedStyle     lipgloss.Style
	textHintStyle      lipgloss.Style
	accentFgStyle      lipgloss.Style
	runningStyle       lipgloss.Style
	okStyle            lipgloss.Style
	toolNameStyle      lipgloss.Style
	gutterStyle        lipgloss.Style
	bandStyle          lipgloss.Style
	userRailStyle      lipgloss.Style
	codeBandStyle      lipgloss.Style
	cardTitleStyle     lipgloss.Style
	optionSelStyle     lipgloss.Style
	errorTitleStyle    lipgloss.Style

	// Legacy aliases.
	youStyle         lipgloss.Style // the caret and the user's voice: accent
	botStyle         lipgloss.Style // the assistant's voice: role.assistant
	toolStyle        lipgloss.Style // a running tool: state.running
	dimStyle         lipgloss.Style // text.muted
	errStyle         lipgloss.Style // state.error
	warnStyle        lipgloss.Style // state.warn
	growStyle        lipgloss.Style // state.ok
	thinkingStyle    lipgloss.Style // role.thinking
	chromeStyle      lipgloss.Style // text.secondary
	accentStyle      lipgloss.Style // accent, bold
	userPanel        lipgloss.Style // the user band
	shortcutStyle    lipgloss.Style // text.hint
	brandStyle       lipgloss.Style
	metaStyle        lipgloss.Style
	chromeRuleStyle  lipgloss.Style
	statusTitleStyle lipgloss.Style
	selectedRowStyle lipgloss.Style

	toolHeadStyle lipgloss.Style
	diffAddStyle  lipgloss.Style
	diffDelStyle  lipgloss.Style
	gitRepoStyle  lipgloss.Style
	bannerFrame   lipgloss.Style

	// Mode chips carry risk, so their colour is semantic: allow-everything is
	// the error red, auto review a warning, plan the safe accent, normal quiet.
	modeChipBase   lipgloss.Style
	modeChipAlways lipgloss.Style
	modeChipPlan   lipgloss.Style
	modeChipReview lipgloss.Style
	modeChipNormal lipgloss.Style
)

func init() { buildStyles() }

// fgTok is a foreground style for a token, carrying the token's mono attribute
// when colour is off.
func fgTok(id tokenID) lipgloss.Style {
	return withMono(lipgloss.NewStyle().Foreground(tokColor(id)), id)
}

func withMono(s lipgloss.Style, id tokenID) lipgloss.Style {
	if !monoMode {
		return s
	}
	a := tokenTable[id].mono
	if a&monoDim != 0 {
		s = s.Faint(true)
	}
	if a&monoBold != 0 {
		s = s.Bold(true)
	}
	if a&monoItalic != 0 {
		s = s.Italic(true)
	}
	if a&monoUnderline != 0 {
		s = s.Underline(true)
	}
	if a&monoReverse != 0 {
		s = s.Reverse(true)
	}
	return s
}

func buildStyles() {
	assignColors()

	textSecondaryStyle = fgTok(tokTextSecondary)
	textMutedStyle = fgTok(tokTextMuted)
	textHintStyle = fgTok(tokTextHint)
	accentFgStyle = fgTok(tokAccent)
	runningStyle = fgTok(tokStateRunning)
	okStyle = fgTok(tokStateOK)
	toolNameStyle = lipgloss.NewStyle().Bold(true)
	gutterStyle = fgTok(tokTextMuted)
	bandStyle = lipgloss.NewStyle().Background(tokColor(tokSurfaceBand))
	userRailStyle = lipgloss.NewStyle().Foreground(tokColor(tokAccent)).Bold(true)
	codeBandStyle = lipgloss.NewStyle().Background(tokColor(tokSurfaceBand))
	cardTitleStyle = lipgloss.NewStyle().Bold(true)
	optionSelStyle = withMono(lipgloss.NewStyle().Foreground(tokColor(tokAccent)).Background(tokColor(tokSurfaceRaised)).Bold(true), tokSurfaceRaised)
	errorTitleStyle = fgTok(tokStateError).Bold(true)

	youStyle = fgTok(tokAccent).Bold(true)
	botStyle = fgTok(tokRoleAssistant).Bold(true)
	toolStyle = fgTok(tokStateRunning)
	dimStyle = fgTok(tokTextMuted)
	errStyle = fgTok(tokStateError)
	warnStyle = fgTok(tokStateWarn)
	growStyle = fgTok(tokStateOK)
	thinkingStyle = fgTok(tokTextMuted).Italic(true)
	chromeStyle = fgTok(tokTextSecondary)
	accentStyle = fgTok(tokAccent).Bold(true)
	userPanel = lipgloss.NewStyle().Background(tokColor(tokSurfaceBand)).Padding(0, 1)
	shortcutStyle = fgTok(tokTextHint)
	// The brand name is structure, not a signal: bold carries it, colour is
	// saved for the one actionable span in the card.
	brandStyle = fgTok(tokTextSecondary).Bold(true)
	metaStyle = fgTok(tokTextHint)
	chromeRuleStyle = lipgloss.NewStyle().Foreground(tokColor(tokLineRule))
	statusTitleStyle = fgTok(tokTextSecondary).Bold(true)
	selectedRowStyle = optionSelStyle

	toolHeadStyle = lipgloss.NewStyle().Bold(true)
	diffAddStyle = lipgloss.NewStyle().Background(tokColor(tokDiffAddBG))
	diffDelStyle = lipgloss.NewStyle().Background(tokColor(tokDiffDelBG))
	gitRepoStyle = fgTok(tokTextSecondary)
	bannerFrame = lipgloss.NewStyle().
		Border(glyphs.Border).
		BorderForeground(tokColor(tokLineRule)).
		Padding(0, 1)

	modeChipBase = lipgloss.NewStyle().Bold(true).Padding(0, 1)
	chip := func(bg tokenID) lipgloss.Style {
		if monoMode {
			return lipgloss.NewStyle().Bold(true)
		}
		return modeChipBase.Foreground(tokColor(tokChipInk)).Background(tokColor(bg))
	}
	modeChipAlways = chip(tokStateError)
	modeChipPlan = chip(tokAccent)
	modeChipReview = chip(tokStateWarn)
	modeChipNormal = modeChipBase.Foreground(tokColor(tokTextSecondary)).Background(tokColor(tokLineRule))
	if monoMode {
		modeChipNormal = lipgloss.NewStyle()
		// Allow-everything stays the loudest chip even without colour.
		modeChipAlways = lipgloss.NewStyle().Bold(true).Reverse(true)
	}
}
