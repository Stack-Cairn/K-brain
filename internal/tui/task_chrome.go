package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func taskLine(text string) string {
	return strings.Join(strings.Fields(ansi.Strip(text)), " ")
}

func taskColumns(left, right string, width int) string {
	width = max(width, 1)
	right = ansi.Truncate(right, max(width-3, 0), "…")
	left = ansi.Truncate(left, max(width-lipgloss.Width(right)-3, 0), "…")
	gap := max(width-lipgloss.Width(left)-lipgloss.Width(right)-2, 1)
	return ansi.Truncate(" "+left+strings.Repeat(" ", gap)+right+" ", width, "…")
}

func taskElapsed(task agent.BackgroundTask, now time.Time) string {
	if task.StartedAt.IsZero() {
		return ""
	}
	end := now
	if task.Status != agent.TaskRunning && !task.EndedAt.IsZero() {
		end = task.EndedAt
	}
	seconds := max(int(end.Sub(task.StartedAt).Seconds()), 0)
	if seconds >= 3600 {
		return fmt.Sprintf("%dh%02dm", seconds/3600, seconds%3600/60)
	}
	if seconds >= 60 {
		return fmt.Sprintf("%dm%02ds", seconds/60, seconds%60)
	}
	return fmt.Sprintf("%ds", seconds)
}

func (m *model) taskState(task agent.BackgroundTask) (string, lipgloss.Style, string) {
	if task.FollowingUp {
		return m.tr("replying"), accentStyle, "◌"
	}
	switch task.Status {
	case agent.TaskDone:
		return m.tr("done"), growStyle, "✓"
	case agent.TaskError:
		return m.tr("error"), errStyle, "✗"
	case agent.TaskCancelled:
		return m.tr("cancelled"), dimStyle, "○"
	default:
		return m.tr("running"), accentStyle, "◌"
	}
}

func taskUsageTokens(task agent.BackgroundTask) int {
	return task.SubUsage.PromptTokens + task.SubUsage.CompletionTokens
}

func (m *model) taskSummary(tasks []agent.BackgroundTask) string {
	counts := make(map[string]int)
	for _, task := range tasks {
		state := string(task.Status)
		if task.FollowingUp {
			state = "running"
		}
		counts[state]++
	}
	var parts []string
	for _, state := range []string{"running", "error", "done", "cancelled"} {
		if counts[state] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[state], m.tr(state)))
			if m.width < 60 {
				break
			}
		}
	}
	return strings.Join(parts, " · ")
}

func (m *model) taskDockRow(task agent.BackgroundTask, selected bool) string {
	state, style, icon := m.taskState(task)
	marker := " "
	if selected {
		marker = "▎"
	}
	left := marker + " " + style.Render(icon) + " " + task.ID + "  " + taskLine(task.Description)
	right := style.Render(state)
	if elapsed := taskElapsed(task, time.Now()); m.width >= 60 && elapsed != "" {
		right += dimStyle.Render(" · " + elapsed)
	}
	if m.width >= 100 && taskUsageTokens(task) > 0 {
		right += dimStyle.Render(" · " + fmtUsage(task.SubUsage))
	}
	line := taskColumns(left, right, m.width)
	if selected {
		line = selectedRowStyle.Render(ansi.Strip(line))
	}
	return line
}

func (m *model) taskViewChromeRows() int {
	const detailHeaderRows, composerRows, footerRows = 2, 1, 2
	return m.vpTopRows() + detailHeaderRows + composerRows + kbrainPromptFrame().GetVerticalFrameSize() + footerRows
}

func (m *model) taskViewView() string {
	tv := m.taskVP
	task, _ := m.agent.Tasks().Get(tv.id)
	if tv.busy {
		task.FollowingUp = true
	}
	state, style, icon := m.taskState(task)
	head := taskColumns(accentStyle.Render(icon+" "+tv.id)+"  "+taskLine(task.Description), style.Render("("+state+")"), m.width)
	meta := taskLine(task.SubModel)
	if elapsed := taskElapsed(task, time.Now()); elapsed != "" {
		if meta != "" {
			meta += " · "
		}
		meta += elapsed
	}
	if taskUsageTokens(task) > 0 {
		meta += " · " + fmtUsage(task.SubUsage)
	}
	position := m.tr("latest output")
	if !tv.vp.AtBottom() {
		position = fmt.Sprintf("↑ %d%%", int(tv.vp.ScrollPercent()*100))
	}
	meta = taskColumns(dimStyle.Render(meta), dimStyle.Render(position), m.width)
	hint := m.tr("Esc Back")
	switch {
	case task.Restored:
		tv.input.Placeholder = m.tr("restored session — read-only")
	case task.FollowingUp:
		tv.input.Placeholder = m.tr("replying — your draft is kept until ready")
		hint += m.tr(" · Ctrl+X Cancel")
	case task.Status == agent.TaskRunning:
		tv.input.Placeholder = m.tr("send instructions to this running subagent")
		hint += m.tr(" · Enter steer · Ctrl+X Cancel")
	default:
		tv.input.Placeholder = m.tr("message this subagent (Enter to send)")
		hint += m.tr(" · Enter Send")
	}
	hint += m.tr(" · PgUp/PgDn Scroll · Ctrl+End Latest")
	input := tv.input.View()
	if task.Restored {
		input = dimStyle.Render(tv.input.Placeholder)
	}
	input = ansi.Truncate(input, max(m.width-chromeIndent, 1), "…")
	frame := kbrainPromptFrame().Width(max(m.width, 1)).Render(input)
	lines := []string{head, meta, sanitizeView(tv.vp.View()), frame,
		shortcutStyle.Render(strings.Repeat(" ", chromeIndent) + hint), m.statusView()}
	out := strings.Join(lines, "\n")
	rows := strings.Split(out, "\n")
	for i := range rows {
		rows[i] = ansi.Truncate(rows[i], max(m.width, 1), "…")
	}
	return strings.Join(rows, "\n")
}
