package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// chromeIndent is the left gutter the composer and the info rows share, so the
// two rules and everything between them line up.
const chromeIndent = 2

// chromeColumns packs a left and a right cell into one width-exact row with the
// right cell flush against the right edge. The left cell is truncated first so
// the right cell — model, effort, spend — survives narrow terminals.
func chromeColumns(left, right string, width int) string {
	if width <= 0 {
		return ""
	}
	lead := min(chromeIndent, width)
	right = ansi.Truncate(right, max(width-lead-1, 0), "…")
	left = ansi.Truncate(left, max(width-lead-lipgloss.Width(right)-1, 0), "…")
	pad := max(width-lead-lipgloss.Width(left)-lipgloss.Width(right), 0)
	return ansi.Truncate(strings.Repeat(" ", lead)+left+strings.Repeat(" ", pad)+right, width, "")
}

// footerHints is the first of the two info rows under the input box: permission
// mode and shortcuts on the left, the active model and reasoning effort on the
// right.
func (m *model) footerHints() string {
	row, _ := m.footerHintsAt()
	return row
}

// footerHintsAt also reports the column where the clickable effort chip starts,
// or -1 when the row does not carry one (task focus, or the chip truncated away).
func (m *model) footerHintsAt() (string, int) {
	if m.tasksFocus && len(m.dockTasks()) > 0 {
		hint := m.tr("Ctrl+T Input · ↑/↓ Select · Space Preview · Enter Open")
		if m.taskExpanded {
			hint = m.tr("Ctrl+T Input · ↑/↓ Select · Space Collapse · Enter Open")
		}
		return ansi.Truncate(strings.Repeat(" ", min(chromeIndent, max(m.width, 0)))+shortcutStyle.Render(hint), max(m.width, 0), "…"), -1
	}
	left := m.modeChip() + dimStyle.Render(" · ") + shortcutStyle.Render(m.tr("Shift+Tab Mode  ·  Ctrl+C Cancel  ·  Ctrl+P Menu"))
	row := chromeColumns(left, m.footerModelCell(), m.width)

	chipX := -1
	if m.agent != nil {
		if chip := m.effortChip(); strings.HasSuffix(ansi.Strip(row), chip) {
			chipX = lipgloss.Width(ansi.Strip(row)) - lipgloss.Width(chip)
		}
	}
	return row, chipX
}

// footerModelCell is the right-hand cell of the first info row: the model
// name and, when an agent is attached, the reasoning-effort chip.
func (m *model) footerModelCell() string {
	cell := chromeStyle.Render(m.modelName)
	if m.agent == nil {
		return cell
	}
	return cell + dimStyle.Render(" · ") + accentStyle.Render(m.effortChip())
}

func (m *model) effortChip() string {
	return "✦ " + m.tr(effortLabel(m.agent.Effort))
}

// footerRule separates the hints row from the status row. It doubles as the
// footerRule separates the hints row from the status row with a quiet rule that
// starts after the shared gutter indent, lining up with the workspace row.
func (m *model) footerRule() string {
	width := max(m.width, 0)
	if width == 0 {
		return ""
	}
	lead := min(chromeIndent, width)
	return strings.Repeat(" ", lead) + chromeRuleStyle.Render(strings.Repeat("─", width-lead))
}

// fixedFrameRows is what viewBody always spends outside the transcript and the
// composer: the hints row, the rule under it and the status row. The composer's
// top rule doubles as the separator above it, so no blank row is reserved there.
const fixedFrameRows = 3

// composerRows is how many rows the input box occupies, including its rules.
func (m *model) composerRows() int {
	switch {
	case m.iactive != nil:
		return 0
	case m.ancientInput():
		return m.input.Height()
	default:
		return m.input.Height() + kbrainPromptFrame().GetVerticalFrameSize()
	}
}

// kbrainPromptFrame brackets the composer with a rule above and below instead of
// boxing it in, so the input reads as a band across the terminal. The rules take
// the theme accent colour.
func kbrainPromptFrame() lipgloss.Style {
	return lipgloss.NewStyle().
		Border(lipgloss.Border{Top: "─", Bottom: "─"}, true, false).
		BorderForeground(accentColor).
		Padding(0, 0, 0, chromeIndent)
}
