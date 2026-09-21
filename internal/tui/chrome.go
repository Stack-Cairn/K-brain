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
	left = strings.TrimSpace(left)
	brand, detail, found := strings.Cut(left, " · ")
	leftCell := brandStyle.Render(brand)
	if found {
		leftCell += dimStyle.Render(" · ") + chromeStyle.Render(detail)
	}
	rightText := strings.TrimSpace(ansi.Strip(right))
	rightCell := accentStyle.Render(ansi.Truncate(rightText, max(width-3, 0), "…"))
	leftCell = ansi.Truncate(leftCell, max(width-lipgloss.Width(rightCell)-3, 0), "…")
	pad := max(width-lipgloss.Width(leftCell)-lipgloss.Width(rightCell)-2, 1)
	line := " " + leftCell + strings.Repeat(" ", pad) + rightCell + " "
	line = ansi.Truncate(line, width, "…")
	rule := chromeRuleStyle.Render(strings.Repeat("─", width))
	if width > 24 {
		hint := "  ctrl+p · " + label
		hint = ansi.Truncate(hint, width, "…")
		rule = ansi.Truncate(rule, max(width-lipgloss.Width(hint), 0), "") + shortcutStyle.Render(hint)
	}
	return line + "\n" + rule
}

func (m *model) footerHints() string {
	if m.tasksFocus && len(m.dockTasks()) > 0 {
		hint := m.tr("ctrl+t input · ↑/↓ select · space preview · enter open")
		if m.taskExpanded {
			hint = m.tr("ctrl+t input · ↑/↓ select · space collapse · enter open")
		}
		return ansi.Truncate(" "+shortcutStyle.Render(hint), max(m.width, 0), "…")
	}
	mode := accentStyle.Render(m.permissionModeLabel())
	hints := shortcutStyle.Render(m.tr("shift+tab mode  ·  ctrl+c cancel  ·  ctrl+p menu"))
	return ansi.Truncate(" "+mode+dimStyle.Render("  ·  ")+hints, max(m.width, 0), "…")
}

func kbrainPromptFrame(height int) lipgloss.Style {
	border := lipgloss.RoundedBorder()
	if height > 0 && height < 28 {
		border = lipgloss.Border{Left: "│", Right: "│"}
	}
	full := height <= 0 || height >= 28
	return lipgloss.NewStyle().Border(border, full, true, full, true).BorderForeground(lipgloss.AdaptiveColor{Light: "153", Dark: "60"}).Padding(0, 1)
}
