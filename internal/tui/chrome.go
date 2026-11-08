package tui

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	schedule "github.com/Stack-Cairn/K-brain/internal/scheduler"
	"github.com/Stack-Cairn/K-brain/internal/session"
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

// statusLeft is the left half of the info row after the mode chip: the git
// readout, then the goal. The working directory lives in the banner instead.
func (m *model) statusLeft() string {
	if m.transientNotice != "" {
		return dimStyle.Render(ansi.Truncate(m.transientNotice, max(m.width/2, 16), "…"))
	}
	if line := m.statusLineText(); line != "" {
		return dimStyle.Render(line)
	}
	left := m.git.render()
	if m.goal != "" {
		if left != "" {
			left += "  "
		}
		left += dimStyle.Render("◎ " + truncLine(m.goal, 32))
	}
	if bg := m.backgroundCell(); bg != "" {
		if left != "" {
			left += "  "
		}
		left += bg
	}
	return left
}

// footerStats is the quiet run readout in front of the model cell. Each item
// only appears when it says something: no zero costs, no 0% scroll.
func (m *model) footerStats() string {
	var parts []string
	if m.agent != nil && m.agent.ContextLimit > 0 {
		parts = append(parts, m.contextCell())
	}
	if cost, ok := m.sessionCost(); ok && cost > 0 {
		parts = append(parts, fmtCost(cost))
	} else if u := m.agent.TotalUsage(); m.agent != nil && u.PromptTokens+u.CompletionTokens > 0 {
		// Unpriced routes still get a sense of spend.
		parts = append(parts, fmtTok(u.PromptTokens+u.CompletionTokens)+" tok")
	}
	if !m.follow && !m.vp.AtBottom() {
		parts = append(parts, fmt.Sprintf("↑ %d%%", int(m.vp.ScrollPercent()*100)))
	}
	if n := m.runningSubagents(); n > 0 {
		parts = append(parts, fmt.Sprintf("⚙ %d sub", n))
	}
	return strings.Join(parts, " · ")
}

// contextBarCells is how many cells the footer's context bar spans.
const contextBarCells = 6

// contextCell is the footer's context readout: a bar and the percentage on
// a wide footer, the percentage alone when the footer is tight.
func (m *model) contextCell() string {
	pct := contextPercent(m.agent.ContextTokensNow(), m.agent.ContextLimit)
	label := fmt.Sprintf(m.tr("%d%% ctx"), pct)
	if m.width < 90 {
		return label
	}
	return contextBar(pct, contextBarCells) + " " + label
}

func contextPercent(tokens, limit int) int {
	if limit <= 0 {
		return 0
	}
	return min(max(tokens*100/limit, 0), 999)
}

// contextBar draws pct as cells filled cells: state.ok up to 70%, then
// state.warn, then state.error from 90%. The glyphs carry the level when
// colour is off.
func contextBar(pct, cells int) string {
	filled := min((pct*cells+50)/100, cells)
	if pct > 0 && filled == 0 {
		filled = 1
	}
	tone := okStyle
	switch {
	case pct >= 90:
		tone = errStyle
	case pct >= 70:
		tone = warnStyle
	}
	return tone.Render(strings.Repeat(glyphs.BarFull, filled)) + chromeRuleStyle.Render(strings.Repeat(glyphs.BarEmpty, cells-filled))
}

func shortCWD() string {
	dir := cwd()
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(dir, home) {
		dir = "~" + strings.TrimPrefix(dir, home)
	}
	parts := strings.Split(strings.Trim(dir, "/"), "/")
	if len(parts) > 3 {
		return "…/" + strings.Join(parts[len(parts)-3:], "/")
	}
	return dir
}

// scheduleCache is the soonest scheduled wakeup of this session, re-read from
// the store every few seconds rather than on every frame.
type scheduleCache struct {
	read  time.Time
	sid   string
	next  time.Time
	count int
}

const scheduleCacheTTL = 2 * time.Second

func (c *scheduleCache) refresh(m *model, now time.Time) {
	if m.store == nil || m.sessionID == "" {
		*c = scheduleCache{}
		return
	}
	if c.sid == m.sessionID && now.Sub(c.read) < scheduleCacheTTL {
		return
	}
	c.read, c.sid, c.next, c.count = now, m.sessionID, time.Time{}, 0
	for _, sc := range m.store.Schedules(m.sessionID) {
		next, ok := scheduleNext(sc)
		if !ok {
			continue
		}
		c.count++
		if c.next.IsZero() || next.Before(c.next) {
			c.next = next
		}
	}
}

// scheduleNext is when a schedule fires next, the same slot fireDueSchedules
// works from; false once a one-shot has fired.
func scheduleNext(sc session.Schedule) (time.Time, bool) {
	s, err := schedule.Parse(sc.Schedule)
	if err != nil {
		return time.Time{}, false
	}
	if sc.LastFire.IsZero() {
		return sc.Anchor, true
	}
	return s.NextAfter(sc.Anchor, sc.LastFire)
}

// fmtUntil is a short countdown: 45s, 2m, 1h05m, 3d.
func fmtUntil(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", max(int(d.Seconds()), 0))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// scheduleNextNote is the "next in 2m" tail of a /schedule list entry.
func (m *model) scheduleNextNote(sc session.Schedule) string {
	next, ok := scheduleNext(sc)
	if !ok {
		return ""
	}
	if d := next.Sub(m.nowFn()); d > 0 {
		return dimStyle.Render(" · " + m.tr("next in") + " " + fmtUntil(d))
	}
	return dimStyle.Render(" · " + m.tr("due now"))
}

// backgroundCell is the footer's note on work that will wake the session:
// background waits still polling and the next scheduled wakeup.
func (m *model) backgroundCell() string {
	var parts []string
	if m.agent != nil {
		if n := m.agent.Waits().Active(); n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, plural(n, m.tr("wait"), m.tr("waits"))))
		}
	}
	if c := m.status().sched; c.count > 0 {
		if d := c.next.Sub(m.nowFn()); d > 0 {
			parts = append(parts, m.tr("next in")+" "+fmtUntil(d))
		} else {
			parts = append(parts, m.tr("due now"))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return dimStyle.Render(chromeGlyphs("◐ " + strings.Join(parts, " · ")))
}
