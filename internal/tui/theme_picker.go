package tui

import (
	"fmt"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// The theme picker (/theme, the palette's Theme entry, Enter on the settings
// row). Moving the selection previews the theme on the whole screen without
// saving it; Enter (or ←/→) saves, Esc puts the saved theme back.

// themePanel builds the picker with the saved theme selected. The panel's
// note keeps the saved setting so Esc can restore it.
func (m *model) themePanel() *ppanel {
	m.refreshCustomThemes(true)
	list := themeChoices()
	cur := m.currentThemeName()
	pp := &ppanel{kind: panelTheme, title: "Theme", list: list, note: cur}
	if i := slices.Index(list, cur); i >= 0 {
		pp.midx = i
	} else if ch, ok := parseThemeSetting(cur); ok {
		// A pinned family ("harbor:light") selects its family row.
		if i := slices.Index(list, ch.family); i >= 0 {
			pp.midx = i
		}
	}
	return pp
}

// themePanelKey handles a key in the picker; done says the panel closes.
func (m *model) themePanelKey(msg tea.KeyMsg, pp *ppanel) (done bool) {
	if len(pp.list) == 0 {
		return true
	}
	switch msg.Type {
	case tea.KeyEsc, tea.KeyCtrlC:
		m.previewTheme(pp.note)
		return true
	case tea.KeyUp, tea.KeyCtrlP, tea.KeyShiftTab:
		pp.midx = (pp.midx - 1 + len(pp.list)) % len(pp.list)
		m.previewTheme(pp.list[pp.midx])
	case tea.KeyDown, tea.KeyCtrlN, tea.KeyTab:
		pp.midx = (pp.midx + 1) % len(pp.list)
		m.previewTheme(pp.list[pp.midx])
	case tea.KeyLeft, tea.KeyRight, tea.KeyEnter:
		m.pickTheme(pp.list[pp.midx])
		pp.note = m.currentThemeName()
		return msg.Type == tea.KeyEnter
	}
	return false
}

// themePanelView lists the themes with a line about each, then a sample of
// the previewed theme's signals, code and diff colours.
func (m *model) themePanelView(pp *ppanel) string {
	var b strings.Builder
	w := max(m.width, 1)
	nameW := 0
	for _, name := range pp.list {
		nameW = max(nameW, ansi.StringWidth(name))
	}
	lo, hi := 0, len(pp.list)
	if avail := m.height - 14; avail > 0 && len(pp.list) > avail {
		lo, hi = viewportWindow(len(pp.list), pp.midx, avail)
	}
	errs := customThemeErrors()
	for i, e := range errs {
		if i == 2 && len(errs) > 3 {
			e = fmt.Sprintf("%d more theme files skipped", len(errs)-2)
		}
		b.WriteString(warnStyle.Render(ansi.Truncate(chromeGlyphs("  "+glyphs.Warn+" "+e), w, chromeGlyphs("…"))) + "\n")
		if i == 2 {
			break
		}
	}
	if lo > 0 {
		b.WriteString(dimStyle.Render(fmt.Sprintf(m.tr("   ↑ %d more"), lo)) + "\n")
	}
	for i := lo; i < hi; i++ {
		name := pp.list[i]
		label := name + strings.Repeat(" ", nameW-ansi.StringWidth(name))
		desc := themeChoiceDesc(name)
		if name == pp.note {
			desc += m.tr("  (current)")
		}
		desc = chromeGlyphs(desc)
		var line string
		if i == pp.midx {
			line = botStyle.Render(" "+menuMark()+label) + "  " + dimStyle.Render(desc)
		} else {
			line = "   " + label + "  " + dimStyle.Render(desc)
		}
		b.WriteString(ansi.Truncate(line, w, chromeGlyphs("…")) + "\n")
	}
	if hi < len(pp.list) {
		b.WriteString(dimStyle.Render(fmt.Sprintf(m.tr("   ↓ %d more"), len(pp.list)-hi)) + "\n")
	}
	if t := lookupTheme(pp.list[pp.midx]); t != nil && len(t.warnings) > 0 {
		warn := t.warnings[0]
		if n := len(t.warnings) - 1; n > 0 {
			warn += fmt.Sprintf(" (+%d more)", n)
		}
		b.WriteString(warnStyle.Render(ansi.Truncate(chromeGlyphs("  "+glyphs.Warn+" "+warn), w, chromeGlyphs("…"))) + "\n")
	}
	b.WriteString("\n")
	for _, l := range themeSample() {
		b.WriteString(ansi.Truncate("   "+l, w, chromeGlyphs("…")) + "\n")
	}
	b.WriteString("\n" + dimStyle.Render(ansi.Truncate(chromeGlyphs(m.tr("  ↑/↓ preview · Enter/←/→ apply · Esc back")), w, chromeGlyphs("…"))))
	return b.String()
}

// themeSample is three lines in the active styles: the signal colours, a
// highlighted line of code, and a diff pair.
func themeSample() []string {
	signals := strings.Join([]string{
		youStyle.Render(strings.TrimSpace(glyphUser) + " you"),
		botStyle.Render(glyphs.Assistant + " reply"),
		runningStyle.Render(glyphs.ToolRun + " running"),
		okStyle.Render(glyphs.OK + " ok"),
		warnStyle.Render(glyphs.Warn + " warn"),
		errStyle.Render(glyphs.Fail + " error"),
		dimStyle.Render("muted"),
	}, "  ")
	code := "func total(n int) string { return fmt.Sprint(n * 42) } // sum"
	base := lipgloss.NewStyle()
	if backgroundsActive() {
		base = codeBandStyle
	}
	line := code
	if lx := codeLexer("go", ""); lx != nil {
		if ls := highlightLines(code, lx, base); len(ls) == 1 {
			line = ls[0]
		}
	}
	diff := diffAddStyle.Render("+ added line ") + "  " + diffDelStyle.Render(glyphs.Minus+" removed line ")
	return []string{signals, line, diff}
}
