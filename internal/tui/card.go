package tui

import (
	"fmt"
	"strings"

	"github.com/alecthomas/chroma/v2"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// A blocking card is the one boxed element in the transcript: an approval or a
// question that stops the turn until it is answered. Its border carries the
// semantic colour (warning for approvals, accent for questions) and its title
// sits in the top border, so the card reads as one unit at any width.

// cardWidth is the width a card is drawn at: the terminal's, or 80 before the
// first size message arrives.
func cardWidth(width int) int {
	if width <= 0 {
		return 80
	}
	return max(width, 8)
}

// cardInner is the text width inside a card drawn at width.
func cardInner(width int) int { return max(width-4, 1) }

// renderCard draws title and rows in a full-width card bordered in tone. Rows
// are pre-styled and may hold several lines; a line too long for the card
// wraps inside it rather than losing its tail.
func renderCard(tone tokenID, title string, rows []string, width int) string {
	width = max(width, 8)
	inner := cardInner(width)
	rule := fgTok(tone)
	b := glyphs.Border
	out := []string{borderTitledIn(rule, b.TopLeft, b.Top, b.TopRight, title, "", width)}
	for _, row := range rows {
		for line := range strings.SplitSeq(ansi.Wrap(row, inner, " "), "\n") {
			line = ansi.Truncate(line, inner, "")
			out = append(out, rule.Render(b.Left)+" "+line+strings.Repeat(" ", max(inner-lipgloss.Width(line), 0))+" "+rule.Render(b.Right))
		}
	}
	out = append(out, borderTitledIn(rule, b.BottomLeft, b.Bottom, b.BottomRight, "", "", width))
	return strings.Join(out, "\n")
}

// cardBand is text on the raised band, filled to the card's inner width so
// the band reads as a block. Without background colour it is indented instead.
func cardBand(text string, width int) string {
	inner := cardInner(width)
	lines := strings.Split(wrap(chromeGlyphs(text), max(inner-2, 1)), "\n")
	for i, l := range lines {
		if !backgroundsActive() {
			lines[i] = "  " + l
			continue
		}
		l = " " + l
		lines[i] = bandStyle.Render(l + strings.Repeat(" ", max(inner-lipgloss.Width(l), 0)))
	}
	return strings.Join(lines, "\n")
}

// cardOption is one numbered choice. The selected row carries the caret, the
// accent and the raised surface; the rest stay plain. A long label wraps
// under itself, clear of the number.
func cardOption(selected bool, num, label string, width int) string {
	lead := "  " + num + "  "
	if selected {
		lead = glyphs.User + " " + num + "  "
	}
	inner := cardInner(width)
	pad := 0
	if selected && (backgroundsActive() || monoMode) {
		pad = 1 // the raised surface reaches one cell past the label
	}
	row := wrapHanging(lead, label, max(inner-pad, 1))
	if !selected {
		return row
	}
	lines := strings.Split(row, "\n")
	for i, l := range lines {
		lines[i] = optionSelStyle.Render(ansi.Truncate(l+strings.Repeat(" ", pad), inner, ""))
	}
	return strings.Join(lines, "\n")
}

// ── Tall cards ───────────────────────────────────────────────────────────
//
// A card may hold more than fits: a long command, an expanded diff, a plan.
// The card keeps its frame, options and hints in view and scrolls only its
// body, so the composer and the choices never leave the screen.

// cardMaxHeight is the most rows a blocking card may take: the screen less
// the composer, the info and busy rows and a few transcript rows. Before the
// first size message there is no limit.
func (m *model) cardMaxHeight() int {
	if m.height <= 0 {
		return 1 << 16
	}
	reserve := fixedFrameRows + m.composerRows() + 2 + 1 + 3
	return max(m.height-reserve, 8)
}

// cardLines wraps rows to the card's inner width as renderCard would and
// returns them one line each, so their count is the rows they take.
func cardLines(rows []string, width int) []string {
	inner := cardInner(width)
	var out []string
	for _, row := range rows {
		out = append(out, strings.Split(ansi.Wrap(row, inner, " "), "\n")...)
	}
	return out
}

// cardScroll is a card body's scroll position.
type cardScroll struct {
	off  int // first body line shown
	page int // body lines shown at the last render
	over int // lines past the window at the last render
}

// window is lines cut to at most n rows from the scroll position. When lines
// are hidden the last row says which are shown and how to move.
func (s *cardScroll) window(lines []string, n int) []string {
	n = max(n, 2)
	if len(lines) <= n {
		s.off, s.page, s.over = 0, len(lines), 0
		return lines
	}
	n--
	s.off = min(max(s.off, 0), len(lines)-n)
	s.page, s.over = n, len(lines)-n
	out := append([]string(nil), lines[s.off:s.off+n]...)
	pos := fmt.Sprintf("%s %d-%d / %d · PgUp/PgDn", glyphs.Fold, s.off+1, s.off+n, len(lines))
	return append(out, textHintStyle.Render(chromeGlyphs(pos)))
}

// scrolled reports whether the last render hid lines.
func (s *cardScroll) scrolled() bool { return s.over > 0 }

// by moves the window by delta lines, kept in range.
func (s *cardScroll) by(delta int) { s.off = min(max(s.off+delta, 0), s.over) }

// show moves the window just enough to bring line i into view.
func (s *cardScroll) show(i int) {
	if s.page <= 0 {
		return
	}
	if i < s.off {
		s.off = i
	} else if i >= s.off+s.page {
		s.off = i - s.page + 1
	}
	s.off = min(max(s.off, 0), s.over)
}

// cardScrollKey moves s for PgUp/PgDn (a page) and reports whether the key
// was one of them.
func cardScrollKey(s *cardScroll, msg tea.KeyMsg) bool {
	switch msg.Type {
	case tea.KeyPgUp:
		s.by(-max(s.page-1, 1))
	case tea.KeyPgDown:
		s.by(max(s.page-1, 1))
	default:
		return false
	}
	return true
}

// cardRunes splits a burst of typed characters (keys that arrived together)
// into one key each, so a quick "2" then a comment reach the card in order:
// the first key may open a text box the rest belong to. It reports whether
// msg was such a burst; each key goes to press, until press reports the card
// closed.
func cardRunes(msg tea.KeyMsg, press func(tea.KeyMsg) bool) bool {
	if msg.Type != tea.KeyRunes || len(msg.Runes) < 2 || msg.Paste {
		return false
	}
	for _, r := range msg.Runes {
		if !press(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}) {
			break
		}
	}
	return true
}

// cardCode is code on the card's band, coloured by lexer (nil: plain) and
// wrapped to the band so every character is shown.
func cardCode(code string, lexer chroma.Lexer, width int) []string {
	inner := cardInner(width)
	tinted := backgroundsActive()
	base := lipgloss.NewStyle()
	if tinted {
		base = bandStyle
	}
	var out []string
	for _, line := range highlightLines(strings.TrimRight(code, "\n"), lexer, base) {
		for l := range strings.SplitSeq(ansi.Wrap(line, max(inner-2, 1), " "), "\n") {
			if !tinted {
				out = append(out, "  "+l)
				continue
			}
			pad := max(inner-1-lipgloss.Width(l), 0)
			out = append(out, bandStyle.Render(" ")+l+bandStyle.Render(strings.Repeat(" ", pad)))
		}
	}
	return out
}

// cardChoice is a row of alternatives with the current one marked: accented
// and bold with colour, bracketed without.
func cardChoice(items []string, cur int) string {
	parts := make([]string, len(items))
	for i, it := range items {
		switch {
		case i != cur:
			parts[i] = textMutedStyle.Render(it)
		case monoMode:
			parts[i] = cardTitleStyle.Render("[" + it + "]")
		default:
			parts[i] = cardTitleStyle.Inherit(accentFgStyle).Render(it)
		}
	}
	return strings.Join(parts, textHintStyle.Render(chromeGlyphs(" · ")))
}
