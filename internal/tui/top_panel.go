package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/Stack-Cairn/K-brain/internal/agent"
)

// The top panel: the session's work in progress, pinned above the
// transcript. Collapsed it is one titled rule summing up the goal, the plan
// and the subagents; expanded it lists them, one row each.
//
//	── ▾ ⠋ Goal · Executing · Ship the release · round 2/100 · 1m02s ── ☰ Plan 2/4 ── ◆ 2 running · 1 done ── Alt+T ─
//	   ◎ Ship the release checklist                                     round 2/100 · 1m02s
//	   ☰ Plan 2/4 · ◐ Draft the release notes
//	   ◌ sub-3  Draft release notes · Grep(feat)                       running · 1 tool · 12s
//	   ✓ sub-1  Audit changelog entries                                   done · 2 tools · 9s
//	────────────────────────────────────────────────────────────────────────────────────────────
//
// Alt+T or a click on the header toggles it, and the choice holds for the
// session. Until the user picks, it opens by itself when subagents start or
// the agent writes a plan, and folds again once the turn and its subagents
// have settled.

// topPanelState is the panel's state, kept on the model.
type topPanelState struct {
	// expanded is whether the panel lists its rows; userSet records that the
	// user chose, which turns the automatic opening and folding off.
	expanded, userSet bool
	// autoOpened marks an expansion the panel made by itself, which it folds
	// again when the work settles.
	autoOpened bool
	// wasLive is whether work was in flight at the last layout.
	wasLive bool

	// rows is how many rows the panel took at the last layout, its closing
	// rule included, and drawn how many the last frame drew (0 while a full
	// screen view covers it); dockOff is the row its subagent rows start on.
	rows, drawn, dockOff int

	// goalSince is when the current goal was set; goalState is "" while the
	// goal runs or idles, "waiting" for the user's reply or "paused" at the
	// round limit.
	goalSince time.Time
	goalState string

	// turnSeq counts the turns started; planSeq is the turn that last
	// reported the plan, 0 when the plan on screen is not this run's.
	turnSeq, planSeq int
}

const (
	// topPanelMinHeight is the shortest terminal that shows the panel at
	// all; below topPanelExpandHeight it stays one collapsed row.
	topPanelMinHeight    = 6
	topPanelExpandHeight = 12
	topPanelMinWidth     = 24
	// topPanelMaxRows caps the expanded panel on a tall terminal; on others
	// it takes at most topPanelShare percent of the height.
	topPanelMaxRows = 12
	topPanelShare   = 30
)

// topPanelBudget is the most rows the expanded panel may take, rules
// included.
func (m *model) topPanelBudget() int {
	return min(topPanelMaxRows, max(3, m.height*topPanelShare/100))
}

// panelExpanded reports whether the panel lists its rows now: by choice or
// while the subagent rows have the keyboard, and only with room for them.
func (m *model) panelExpanded() bool {
	return (m.tp.expanded || m.tasksFocus) && m.height >= topPanelExpandHeight
}

// panelPlan is the plan the panel shows: only one the agent reported in this
// run, so a resumed or replaced session's list never resurfaces.
func (m *model) panelPlan() []agent.Todo {
	if m.tp.planSeq == 0 {
		return nil
	}
	return m.todoItems()
}

// topPanelShown reports whether the panel has something to show and room to
// show it.
func (m *model) topPanelShown() bool {
	if m.height < topPanelMinHeight || m.width < topPanelMinWidth {
		return false
	}
	return m.goal != "" || len(m.panelPlan()) > 0 || len(m.dockTasks()) > 0
}

// toggleTopPanel flips the panel for the rest of the session.
func (m *model) toggleTopPanel() {
	m.tp.expanded = !m.panelExpanded()
	m.tp.userSet, m.tp.autoOpened = true, false
	if !m.tp.expanded {
		m.tasksFocus, m.taskExpanded = false, false
	}
	m.layout()
}

// syncTopPanel opens the panel when work starts and folds it when that work
// has settled, unless the user has chosen for themselves.
func (m *model) syncTopPanel() {
	running := m.runningSubagents()
	plan := m.panelPlan()
	// A plan under review is read in its card; the panel keeps to its header.
	live := running > 0 || (m.busy && !m.planOpen() && m.tp.planSeq == m.tp.turnSeq && slices.ContainsFunc(plan, todoOpen))
	if !m.tp.userSet {
		switch {
		case live && !m.tp.wasLive && !m.tp.expanded:
			m.tp.expanded, m.tp.autoOpened = true, true
		case !live && (!m.busy || m.planOpen()) && m.tp.autoOpened && !m.tasksFocus:
			m.tp.expanded, m.tp.autoOpened = false, false
		}
	}
	m.tp.wasLive = live || (m.busy && !m.planOpen() && m.tp.wasLive)
}

// panelTurnStarted is called as a turn starts. A finished plan from an
// earlier turn goes away when the user moves on; one with open steps stays
// as the working plan.
func (m *model) panelTurnStarted(authored bool) {
	m.tp.turnSeq++
	m.tp.goalState = ""
	if authored && m.tp.planSeq != 0 && !slices.ContainsFunc(m.todoItems(), todoOpen) {
		m.tp.planSeq = 0
	}
}

// panelPlanReported records that this turn wrote the plan.
func (m *model) panelPlanReported() { m.tp.planSeq = max(m.tp.turnSeq, 1) }

// resetTopPanel forgets the session's goal timing and plan when the session
// changes; the user's open/closed choice stays.
func (m *model) resetTopPanel() {
	m.tp.goalSince, m.tp.goalState = time.Time{}, ""
	m.tp.planSeq, m.tp.wasLive, m.tp.autoOpened = 0, false, false
	if !m.tp.userSet {
		m.tp.expanded = false
	}
}

// topPanelRows is the panel's height in the chrome, measured at layout.
func (m *model) topPanelRows() int { return m.tp.rows }

// measureTopPanel renders the panel once to learn its height, which the
// chrome and the dock hit-testing read back.
func (m *model) measureTopPanel() {
	m.syncTopPanel()
	m.dockRows, m.dockSkip = 0, 0
	if v := m.topPanelView(); v != "" {
		m.tp.rows = lipgloss.Height(v)
		return
	}
	m.tp.rows = 0
}

// topPanelSection is the panel as viewBody draws it, ending in a newline.
func (m *model) topPanelSection() string {
	if v := m.topPanelView(); v != "" {
		return v + "\n"
	}
	return ""
}

// topPanelView is the panel, or "" while it is hidden. It also records where
// the subagent rows sit, for the mouse and the dock's selection.
func (m *model) topPanelView() string {
	m.dockOffsets = nil
	if !m.topPanelShown() {
		return ""
	}
	width := max(m.width, 1)
	expanded := m.panelExpanded()
	tasks := m.dockTasks()
	plan := m.panelPlan()

	rows := []string{m.panelHeader(expanded, tasks, plan, width)}
	if !expanded {
		if asciiMode {
			return ansi.Truncate(chromeGlyphs(rows[0]), width, "")
		}
		return rows[0]
	}
	budget := m.topPanelBudget() - 2 // the header and the closing rule
	if m.goal != "" && budget > 0 {
		rows = append(rows, m.panelGoalRow(width))
		budget--
	}
	if len(plan) > 0 && budget > 0 {
		rows = append(rows, m.panelPlanRow(plan, width))
		budget--
		// With no subagents to list the plan's own steps fill the room.
		if len(tasks) == 0 && !m.tasksFocus {
			for _, it := range planWindow(plan, budget) {
				rows = append(rows, truncCells("     "+todoRow(it), width))
				budget--
			}
		}
	}
	if len(tasks) > 0 && budget > 0 {
		m.tp.dockOff = len(rows)
		m.dockBudget = budget
		dock := m.tasksDock()
		m.dockBudget = 0
		if dock != "" {
			m.dockRows = lipgloss.Height(dock)
			if m.tasksFocus {
				m.dockSkip = 1
			}
			rows = append(rows, strings.Split(dock, "\n")...)
		}
	}
	rows = append(rows, chromeRuleStyle.Render(strings.Repeat(glyphs.Border.Top, width)))
	if asciiMode {
		for i, r := range rows {
			rows[i] = ansi.Truncate(chromeGlyphs(r), width, "")
		}
	}
	return strings.Join(rows, "\n")
}

// planWindow is up to n plan steps around the current one: the step just
// finished, then what is open.
func planWindow(items []agent.Todo, n int) []agent.Todo {
	if n <= 0 || len(items) == 0 {
		return nil
	}
	start := 0
	if open := slices.IndexFunc(items, todoOpen); open > 0 {
		start = open - 1
	}
	if len(items)-start < n {
		start = max(len(items)-n, 0)
	}
	return items[start:min(start+n, len(items))]
}

// panelHeader is the panel's first row: a rule carrying the goal, plan and
// subagent summaries, and the toggle hint at its end. Collapsed, the goal's
// objective and timing fill what room the other chips leave.
func (m *model) panelHeader(expanded bool, tasks []agent.BackgroundTask, plan []agent.Todo, width int) string {
	fill := glyphs.Border.Top
	sep := chromeRuleStyle.Render(" " + fill + fill + " ")
	arrow := "▸"
	if expanded {
		arrow = "▾"
	}
	var rest []string
	if len(plan) > 0 {
		rest = append(rest, m.planChip(plan))
	}
	// While the rows have the keyboard the dock's own header counts them.
	if len(tasks) > 0 && !(expanded && m.tasksFocus) {
		rest = append(rest, accentFgStyle.Render(chromeGlyphs("◆"))+" "+textSecondaryStyle.Render(m.taskSummary(tasks)))
	}
	build := func(goal string) string {
		chips := rest
		if goal != "" {
			chips = append([]string{goal}, rest...)
		}
		return chromeRuleStyle.Render(fill+fill+" ") + accentFgStyle.Render(chromeGlyphs(arrow)) + " " + strings.Join(chips, sep)
	}
	hint := ""
	if width >= 80 {
		label := m.tr("Alt+T expand")
		if expanded {
			label = m.tr("Alt+T collapse")
		}
		hint = " " + textHintStyle.Render(label) + " " + chromeRuleStyle.Render(fill+fill)
	}
	room := width - lipgloss.Width(hint)
	goal := ""
	if m.goal != "" {
		goal = m.goalChip(0, false)
		if !expanded {
			const minTitle, dot = 12, 3
			spare := room - 3 - lipgloss.Width(build(goal))
			metaW := dot + lipgloss.Width(m.goalMeta())
			switch {
			case spare >= metaW+dot+minTitle:
				goal = m.goalChip(min(40, spare-metaW-dot), true)
			case spare >= dot+minTitle:
				goal = m.goalChip(min(40, spare-dot), false)
			}
		}
	}
	left := ansi.Truncate(build(goal), max(room-1, 1), "…")
	line := left
	if rest := room - lipgloss.Width(left); rest >= 2 {
		line += " " + chromeRuleStyle.Render(strings.Repeat(fill, rest-1))
	}
	return ansi.Truncate(line+hint, width, "")
}

// goalLabel is the goal's state in a word or two, and its style.
func (m *model) goalLabel() (string, lipgloss.Style) {
	switch {
	case m.tp.goalState == "paused":
		return m.tr("Paused"), errStyle
	case m.tp.goalState == "waiting":
		return m.tr("Waiting for you"), warnStyle
	case m.busy:
		return m.tr("Executing"), runningStyle
	}
	return m.tr("Idle"), textMutedStyle
}

// goalChip is the goal in the header: its state, then up to title cells of
// the objective and, with meta, its round and time.
func (m *model) goalChip(title int, meta bool) string {
	label, style := m.goalLabel()
	mark := accentFgStyle.Render(chromeGlyphs("◎"))
	if m.busy && m.tp.goalState == "" {
		mark = m.spin.View()
	}
	chip := mark + " " + textSecondaryStyle.Render(m.tr("Goal")) + " " + style.Render(label)
	if title <= 0 {
		return chip
	}
	chip += textMutedStyle.Render(chromeGlyphs(" · ")) + textSecondaryStyle.Render(ansi.Truncate(taskLine(m.goal), title, "…"))
	if meta {
		chip += textMutedStyle.Render(chromeGlyphs(" · " + m.goalMeta()))
	}
	return chip
}

// goalMeta is the goal's round and how long it has been running.
func (m *model) goalMeta() string {
	parts := []string{fmt.Sprintf(m.tr("round %d/%d"), m.goalRounds+1, m.goalMaxRounds())}
	if !m.tp.goalSince.IsZero() {
		parts = append(parts, fmtSpan(m.nowFn().Sub(m.tp.goalSince)))
	}
	return strings.Join(parts, " · ")
}

// panelGoalRow is the expanded panel's goal row.
func (m *model) panelGoalRow(width int) string {
	label, style := m.goalLabel()
	left := "  " + accentFgStyle.Render(chromeGlyphs("◎")) + " " + textSecondaryStyle.Render(taskLine(m.goal))
	right := style.Render(label) + textMutedStyle.Render(chromeGlyphs(" · "+m.goalMeta()))
	return taskColumns(left, right, width)
}

// planCounts is how many steps are done (completed or cancelled) of all.
func planCounts(items []agent.Todo) (done, total int) {
	for _, it := range items {
		if it.Status == "completed" || it.Status == "cancelled" {
			done++
		}
	}
	return done, len(items)
}

// planChip is the plan in the header: "☰ Plan 2/4", with a check once every
// step is done.
func (m *model) planChip(items []agent.Todo) string {
	done, total := planCounts(items)
	chip := accentFgStyle.Render(chromeGlyphs("☰")) + " " + textSecondaryStyle.Render(m.tr("Plan")) + " " + textMutedStyle.Render(fmt.Sprintf("%d/%d", done, total))
	if done == total {
		chip += " " + okStyle.Render(chromeGlyphs("✓"))
	}
	return chip
}

// panelPlanRow is the expanded panel's plan row: the counts and the step in
// progress.
func (m *model) panelPlanRow(items []agent.Todo, width int) string {
	row := "   " + m.planChip(items)
	cur := slices.IndexFunc(items, func(t agent.Todo) bool { return t.Status == "in_progress" })
	if cur < 0 {
		cur = slices.IndexFunc(items, todoOpen)
	}
	if cur >= 0 {
		row += textMutedStyle.Render(chromeGlyphs(" · ")) + todoRow(items[cur])
	}
	return truncCells(row, width)
}

// topPanelClick handles a press on the panel: the header toggles it, a
// subagent row opens that subagent. It reports whether the press was on the
// panel.
func (m *model) topPanelClick(y int) bool {
	if m.tp.drawn == 0 {
		return false
	}
	row := y - m.viewTop
	if row < 0 || row >= m.tp.drawn {
		return false
	}
	if row == 0 {
		m.toggleTopPanel()
		return true
	}
	if !m.panelExpanded() || len(m.dockOffsets) == 0 {
		return true
	}
	at := row - m.tp.dockOff - m.dockSkip
	if at < 0 || at >= m.dockTaskRows {
		return true
	}
	sel := -1
	for i, off := range m.dockOffsets {
		if off <= at {
			sel = m.dockLo + i
		}
	}
	tasks := m.dockList()
	if sel < 0 || sel >= len(tasks) {
		return true
	}
	m.taskSel = sel
	m.openTask(tasks[sel].ID)
	return true
}

// keptSettledSubagents is how many finished subagents a new turn keeps in
// the panel; older ones are dropped with their journals.
const keptSettledSubagents = 16

// recentSettled is the ids of the n most recent finished subagents.
func (m *model) recentSettled(n int) []string {
	var ids []string
	for _, t := range m.dockTasks() {
		if !m.subRunning(t) && len(ids) < n {
			ids = append(ids, t.ID)
		}
	}
	return ids
}
