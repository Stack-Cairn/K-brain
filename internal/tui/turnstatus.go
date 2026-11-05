package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// busyRow is the one status row above the composer while a turn runs. The
// left cell says what is happening now and for how long: the phase (preparing,
// waiting for the model, thinking, writing), the running tool, a provider
// retry, a compaction, or what the turn is waiting on. The right cell is the
// turn clock, the live output-token counter and the stop key. It stays one row
// at any width: the right cell sheds the counter and shortens the hint before
// the activity loses its room. It is hidden when idle.
func (m *model) busyRow() string {
	left := m.busyLeft()
	rights := m.busyRights()
	if m.width <= 0 {
		return left + " " + rights[0]
	}
	// The activity keeps at least this many cells; the right cell gives way.
	keep := min(24, max(m.width/2, 12))
	right := rights[len(rights)-1]
	for _, r := range rights {
		if m.width-chromeIndent-lipgloss.Width(chromeGlyphs(r))-1 >= keep {
			right = r
			break
		}
	}
	return chromeColumns(left, right, m.width)
}

// busyLeft is the glyph and the activity.
func (m *model) busyLeft() string {
	st := m.status()
	now := m.nowFn()
	switch {
	case m.permDialog != nil:
		return warnStyle.Bold(true).Render(glyphs.ToolQueued) + " " + warnStyle.Render(m.tr("Waiting for your approval"))
	case m.askDialog != nil:
		return accentFgStyle.Bold(true).Render(glyphs.ToolQueued) + " " + accentFgStyle.Render(m.tr("Waiting for your answer"))
	case st.retry != nil:
		return warnStyle.Bold(true).Render(glyphs.Warn) + " " + warnStyle.Render(st.retry.label(m, now))
	case st.compacting:
		return m.spin.View() + " " + runningStyle.Render(m.tr("Compacting context…")) + phaseClock(now.Sub(st.compactSince))
	}
	if act, ok := m.runningToolActivity(); ok {
		return m.spin.View() + " " + runningStyle.Render(act)
	}
	if m.agent != nil && m.agent.WaitingOnSubagents() {
		return m.spin.View() + " " + runningStyle.Render(m.tr("Waiting for subagents"))
	}
	label := strings.TrimPrefix(m.tr(" thinking…"), " ")
	switch st.phase {
	case phasePreparing:
		label = m.tr("Preparing…")
	case phaseWaiting:
		label = m.tr("Waiting for the model…")
	case phaseThinking:
		label = m.tr("Thinking…")
	case phaseWriting:
		label = m.tr("Writing…")
	}
	out := m.spin.View() + " " + runningStyle.Render(label)
	if !st.phaseSince.IsZero() && st.phase != "" && st.phase != phaseTools {
		out += phaseClock(now.Sub(st.phaseSince))
	}
	return out
}

// phaseClock is the time spent in the current phase, once it is worth showing.
func phaseClock(d time.Duration) string {
	if d < time.Second {
		return ""
	}
	return textMutedStyle.Render("  " + fmtSpan(d))
}

// fmtSpan is a short duration: 4s, 1m05s, 1h02m.
func fmtSpan(d time.Duration) string {
	d = max(d, 0).Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

// label is the retry readout: a countdown while the client sleeps, then the
// attempt it is making.
func (r *retryState) label(m *model, now time.Time) string {
	s := m.tr("Retrying…")
	if left := r.at.Add(r.delay).Sub(now); left >= time.Second {
		s = m.tr("Retrying in") + " " + fmtSpan(left)
	}
	if r.max > 0 {
		s += fmt.Sprintf(" (%d/%d)", r.attempt, r.max)
	}
	if r.reason != "" {
		s += " · " + r.reason
	}
	return s
}

// busyRights are the right-cell variants from fullest to tightest.
func (m *model) busyRights() []string {
	st := m.status()
	hint, short := strings.TrimPrefix(m.tr(" · Esc to interrupt"), " · "), "Esc"
	switch {
	case m.iactive != nil:
		hint, short = "bash (interactive) — type to respond · Ctrl+C Ctrl+C to cancel", "Ctrl+C Ctrl+C"
	case m.interrupt1:
		hint, short = strings.TrimPrefix(m.tr(" · Esc again to interrupt"), " · "), "Esc"
	case m.permDialog != nil || m.askDialog != nil:
		hint, short = "", ""
	}
	clock := strings.TrimSpace(m.busyStats())
	var tok string
	if n := st.turnTokens(); n > 0 {
		tok = glyphs.Tokens + fmtTok(n)
	}
	join := func(parts ...string) string {
		var keep []string
		for _, p := range parts {
			if p != "" {
				keep = append(keep, p)
			}
		}
		return textMutedStyle.Render(strings.Join(keep, " · "))
	}
	return []string{
		join(clock, tok, hint),
		join(clock, hint),
		join(clock, short),
		join(clock),
	}
}

// runningToolActivity names the newest running tool call and how long it has
// been going.
func (m *model) runningToolActivity() (string, bool) {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		b := m.blocks[i]
		if b.kind != blockToolRun || !b.toolRunning || b.toolName == "" {
			continue
		}
		s := toolVerb(b.toolName)
		if subj := rowSubject(b.toolName, b.toolArgs); subj != "" {
			s += " " + truncLine(subj, 48)
		}
		if d, ok := toolTimer.running(b.toolID); ok {
			s += "  " + fmtToolDuration(d)
		}
		return s, true
	}
	return "", false
}

// turnSummaryMin is how long a turn without tool calls must run before it
// earns a summary line; shorter chat replies speak for themselves.
const turnSummaryMin = 10 * time.Second

// appendTurnSummary closes a finished turn with one quiet line: how long it
// took, how many tool calls it ran and how many failed, the output tokens and
// what it cost.
func (m *model) appendTurnSummary() {
	st := m.status()
	if st.turnStart.IsZero() || st.lastTurnFailed {
		return
	}
	took := max(m.nowFn().Sub(st.turnStart), 0)
	if st.tools == 0 && took < turnSummaryMin {
		return
	}
	parts := []string{m.tr("Done in") + " " + fmtSpan(took)}
	if st.tools > 0 {
		calls := fmt.Sprintf("%d %s", st.tools, plural(st.tools, "tool call", "tool calls"))
		if st.toolFails > 0 {
			calls += " (" + fmt.Sprintf(m.tr("%d failed"), st.toolFails) + ")"
		}
		parts = append(parts, calls)
	}
	if n := st.turnTokens(); n > 0 {
		parts = append(parts, glyphs.Tokens+fmtTok(n)+" tok")
	}
	if st.costKnown && m.agent != nil {
		if now, ok := m.sessionCost(); ok && now-st.costStart > 0 {
			parts = append(parts, fmtCost(now-st.costStart))
		}
	}
	m.append(textMutedStyle.Render(chromeGlyphs(glyphs.Notice + " " + strings.Join(parts, " · "))))
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
