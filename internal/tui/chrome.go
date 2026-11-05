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
	left, right = chromeGlyphs(left), chromeGlyphs(right)
	tail := chromeGlyphs("…")
	right = ansi.Truncate(right, max(width-lead-1, 0), tail)
	left = ansi.Truncate(left, max(width-lead-lipgloss.Width(right)-1, 0), tail)
	pad := max(width-lead-lipgloss.Width(left)-lipgloss.Width(right), 0)
	return ansi.Truncate(strings.Repeat(" ", lead)+left+strings.Repeat(" ", pad)+right, width, "")
}

// footerHints is the single info row under the input box: permission mode,
// git and goal on the left; context, spend, the model and reasoning effort on
// the right. Shortcuts live in the composer placeholder.
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
		hint += m.tr(" · Ctrl+X Stop")
		return ansi.Truncate(strings.Repeat(" ", min(chromeIndent, max(m.width, 0)))+shortcutStyle.Render(hint), max(m.width, 0), "…"), -1
	}
	return m.infoRowAt()
}

// infoRow is the info row without the task-dock override; the subagent view
// shows it under its own composer.
func (m *model) infoRow() string {
	row, _ := m.infoRowAt()
	return row
}

func (m *model) infoRowAt() (string, int) {
	left := m.modeChip()
	if st := m.statusLeft(); st != "" {
		left += "  " + st
	}
	right := m.footerModelCell()
	if stats := m.footerStats(); stats != "" {
		right = dimStyle.Render(stats+" · ") + right
	}
	row := chromeColumns(left, right, m.width)

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
	return glyphs.Effort + " " + m.tr(effortLabel(m.agent.Effort))
}

// fixedFrameRows is what viewBody always spends outside the transcript and the
// composer: the one info row. The composer's top rule doubles as the separator
// above it, so no blank row is reserved there.
const fixedFrameRows = 1

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

// kbrainPromptFrame opens the composer with a single quiet rule instead of
// boxing it in; the info row underneath closes the band. The rule is chrome,
// not an action, so it takes line.rule — the caret inside carries the accent.
func kbrainPromptFrame() lipgloss.Style {
	return lipgloss.NewStyle().
		Border(lipgloss.Border{Top: glyphs.Border.Top}, true, false, false, false).
		BorderForeground(ruleColor).
		Padding(0, 0, 0, chromeIndent)
}

// titledRule carries the session title on the right of the composer's top rule,
// so the conversation stays named without spending a row on it.
func (m *model) titledRule(frame string) string {
	title := strings.Join(strings.Fields(m.sessTitle), " ")
	width := max(m.width, 0)
	if title == "" || width < 24 {
		return frame
	}
	rule, rest, _ := strings.Cut(frame, "\n")
	title = ansi.Truncate(title, width/2, "…")
	const tail = 2
	lead := width - lipgloss.Width(title) - 2 - tail
	if lead < 4 || lipgloss.Width(rule) != width {
		return frame
	}
	ruleStyle := chromeRuleStyle
	fill := glyphs.Border.Top
	rule = ruleStyle.Render(strings.Repeat(fill, lead)) + " " + statusTitleStyle.Render(title) + " " + ruleStyle.Render(strings.Repeat(fill, tail))
	return rule + "\n" + rest
}
