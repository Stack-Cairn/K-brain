package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func kbrainHeader(width int, left, right string) string {
	return kbrainHeaderLabel(width, left, right, "commands")
}

func kbrainHeaderLabel(width int, left, right, label string) string {
	width = max(width, 1)
	leftCell := accentStyle.Render("◆") + chromeStyle.Render(left)
	rightText := strings.TrimSpace(ansi.Strip(right))
	rightCell := lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "33", Dark: "75"}).Background(lipgloss.AdaptiveColor{Light: "255", Dark: "235"}).Padding(0, 1).Render(rightText)
	pad := max(width-lipgloss.Width(leftCell)-lipgloss.Width(rightCell)-3, 1)
	line := " " + leftCell + strings.Repeat(" ", pad) + rightCell
	line = ansi.Truncate(line, width, "…")
	rule := strings.Repeat("─", width)
	if width > 24 {
		hint := "  ctrl+p  ·  " + label
		rule = ansi.Truncate(rule, max(width-lipgloss.Width(hint), 0), "") + hint
	}
	return line + "\n" + chromeStyle.Render(rule)
}

func kbrainPromptFrame(height int) lipgloss.Style {
	border := lipgloss.RoundedBorder()
	if height > 0 && height < 28 {
		border = lipgloss.Border{Left: "│", Right: "│"}
	}
	full := height <= 0 || height >= 28
	return lipgloss.NewStyle().Border(border, full, true, full, true).BorderForeground(lipgloss.AdaptiveColor{Light: "240", Dark: "238"}).Padding(0, 1)
}
