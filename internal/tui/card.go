package tui

import (
	"strings"

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
