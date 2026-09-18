package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func grokHeader(width int, left, right string) string {
	width = max(width, 1)
	leftCell := accentStyle.Render("◆") + chromeStyle.Render(left)
	rightText := strings.TrimSpace(ansi.Strip(right))
	rightCell := lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "33", Dark: "75"}).Background(lipgloss.AdaptiveColor{Light: "255", Dark: "235"}).Padding(0, 1).Render(rightText)
	pad := max(width-lipgloss.Width(leftCell)-lipgloss.Width(rightCell)-3, 1)
	line := " " + leftCell + strings.Repeat(" ", pad) + rightCell
	line = ansi.Truncate(line, width, "…")
	rule := strings.Repeat("─", width)
	if width > 24 {
		rule = ansi.Truncate(rule, width-22, "") + "  ctrl+p  ·  commands"
	}
	return line + "\n" + chromeStyle.Render(rule)
}

func grokPromptFrame(height int) lipgloss.Style {
	border := lipgloss.RoundedBorder()
	if height > 0 && height < 28 {
		border = lipgloss.Border{Left: "│", Right: "│"}
	}
	return lipgloss.NewStyle().Border(border).BorderForeground(lipgloss.AdaptiveColor{Light: "240", Dark: "238"}).Padding(0, 1)
}
