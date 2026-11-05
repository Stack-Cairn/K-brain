package tui

// The /usage card: the context window, the session's token and cache totals,
// what it cost, and the split by model and subagent. It opens above the
// composer like the other cards and closes with Esc, q or Enter.

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

type usageCard struct{}

// usageCommand opens the card.
func (m *model) usageCommand(args []string) (tea.Model, tea.Cmd) {
	if len(args) > 0 {
		m.append(errStyle.Render("usage: /usage"))
		return m, nil
	}
	m.status().usage = &usageCard{}
	return m, nil
}

func (m *model) usageOpen() bool { return m.st != nil && m.st.usage != nil }

// usageKey handles keys while the card is open: Esc, q, Enter and Ctrl+C
// close it; everything else is held so it cannot reach the composer.
func (m *model) usageKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	if !m.usageOpen() {
		return m, nil, false
	}
	switch msg.String() {
	case "esc", "q", "enter", "ctrl+c", " ":
		m.st.usage = nil
	}
	return m, nil, true
}

// usageCardBlock is the card with its separator rows, or "" while closed.
func (m *model) usageCardBlock() string {
	if !m.usageOpen() {
		return ""
	}
	return "\n" + m.usageView() + "\n"
}

func (m *model) usageCardRows() int {
	if !m.usageOpen() {
		return 0
	}
	return lipgloss.Height(m.usageView()) + 1
}

const usageLabelW = 10

// usageRow is a label column and its value; where the two do not fit on one
// row the value moves under the label.
func usageRow(label, value string, inner int) string {
	head := textMutedStyle.Render(fmt.Sprintf("%-*s", usageLabelW, label))
	if usageLabelW+lipgloss.Width(value) <= inner {
		return head + value
	}
	if label == "" {
		return "  " + value
	}
	return textMutedStyle.Render(label) + "\n  " + value
}

func (m *model) usageView() string {
	w := cardWidth(m.width)
	inner := cardInner(w)
	var rows []string

	if m.agent != nil && m.agent.ContextLimit > 0 {
		tok, limit := m.agent.ContextTokens(), m.agent.ContextLimit
		pct := contextPercent(tok, limit)
		cells := min(20, max(inner-usageLabelW-24, 4))
		rows = append(rows, usageRow(m.tr("Context"), contextBar(pct, cells)+" "+
			fmt.Sprintf("%d%%  %s / %s", pct, fmtTok(tok), fmtTok(limit)), inner))
	} else if m.agent != nil {
		rows = append(rows, usageRow(m.tr("Context"), fmtTok(m.agent.ContextTokens())+" "+textMutedStyle.Render(m.tr("(window unknown)")), inner))
	}

	var sum ai.UsageSummary
	if m.agent != nil {
		sum = m.agent.UsageSummary()
	}
	total := sum.Total
	for _, u := range sum.Subagents {
		total.Add(u)
	}
	rows = append(rows, usageRow(m.tr("Session"), usageTokens(total), inner))
	if c := total.Cached(); c > 0 && total.PromptTokens > 0 {
		rows = append(rows, usageRow(m.tr("Cache"), fmt.Sprintf(m.tr("%s read · %d%% of input"), fmtTok(c), c*100/total.PromptTokens), inner))
	}
	if w := total.PromptCacheWriteTokens; w > 0 {
		rows = append(rows, usageRow("", fmt.Sprintf(m.tr("%s written"), fmtTok(w)), inner))
	}
	if cost, ok := m.sessionCost(); ok && m.agent != nil {
		rows = append(rows, usageRow(m.tr("Cost"), fmtCost(cost), inner))
	} else {
		rows = append(rows, usageRow(m.tr("Cost"), textMutedStyle.Render(m.tr("not priced for this route")), inner))
	}
	if u := m.lastResp; u.PromptTokens+u.CompletionTokens > 0 {
		rows = append(rows, usageRow(m.tr("Last call"), usageTokens(u), inner))
	}
	if st := m.status(); m.busy && !st.turnStart.IsZero() {
		turn := fmtSpan(m.nowFn().Sub(st.turnStart))
		if n := st.turnTokens(); n > 0 {
			turn += " · " + glyphs.Tokens + fmtTok(n)
		}
		if st.tools > 0 {
			turn += fmt.Sprintf(" · %d %s", st.tools, plural(st.tools, "tool call", "tool calls"))
		}
		rows = append(rows, usageRow(m.tr("This turn"), turn, inner))
	}

	if len(sum.Models)+len(sum.Subagents) > 0 {
		rows = append(rows, "", textMutedStyle.Render(m.tr("By model")))
		for _, label := range slices.Sorted(maps.Keys(sum.Models)) {
			rows = append(rows, m.usageModelRow(label, sum.Models[label], false, inner))
		}
		for _, label := range slices.Sorted(maps.Keys(sum.Subagents)) {
			rows = append(rows, m.usageModelRow(label, sum.Subagents[label], true, inner))
		}
	}
	rows = append(rows, "", textHintStyle.Render(m.tr("Esc close")))
	for i, r := range rows {
		rows[i] = chromeGlyphs(r)
	}
	head := cardTitleStyle.Render(m.tr("Usage"))
	return renderCard(tokLineRule, head, rows, w)
}

func usageTokens(u ai.Usage) string {
	s := fmt.Sprintf("in %s · out %s", fmtTok(u.PromptTokens), fmtTok(u.CompletionTokens))
	if c := u.Cached(); c > 0 {
		s = fmt.Sprintf("in %s (%s cached) · out %s", fmtTok(u.PromptTokens), fmtTok(c), fmtTok(u.CompletionTokens))
	}
	return s
}

func (m *model) usageModelRow(label string, u ai.Usage, sub bool, inner int) string {
	name, prov, _ := strings.Cut(label, " @ ")
	if prov == "" && !sub {
		prov = m.provName
	}
	who := name
	if prov != "" {
		who += " @ " + prov
	}
	if sub {
		who = m.tr("subagent") + " " + who
	}
	value := usageTokens(u)
	if lipgloss.Width(value)+4 > inner {
		value = fmt.Sprintf("in %s · out %s", fmtTok(u.PromptTokens), fmtTok(u.CompletionTokens))
	}
	if c, ok := m.usageCost(name, prov, u); ok && c > 0 {
		value += " · " + fmtCost(c)
	}
	return "  " + textSecondaryStyle.Render(ansi.Truncate(who, max(inner-2, 8), "…")) + "\n    " + value
}
