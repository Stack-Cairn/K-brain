package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/config"
)

func (m *model) compactModelLabel() string {
	cm := m.compactModel
	if cm == "" {
		cm = config.DefaultCompactModel
	}
	if _, _, _, err := m.cfg.Resolve(cm, m.compactProv); err == nil {
		if prov := m.compactProv; prov != "" {
			return cm + " @ " + prov
		}
		if mdl := m.cfg.Models[cm]; len(mdl.Providers) > 0 {
			return cm + " @ " + mdl.Providers[0]
		}
		return cm
	}
	return m.modelName + " @ " + m.provName
}

func (m *model) compactResultLine(msg compactMsg) string {
	var b strings.Builder
	fmt.Fprintf(&b, "◎ compacted — summarized %d msgs, %d kept", msg.took, msg.kept)
	if msg.info.Model != "" {
		b.WriteString(" · " + msg.info.Model)
	}
	if cost, ok := m.compactCost(msg.info); ok {
		b.WriteString(" · " + fmtCost(cost))
	}
	if u := msg.info.Usage; u.PromptTokens > 0 || u.CompletionTokens > 0 {
		b.WriteString(" (" + fmtUsage(u) + ")")
	}
	if msg.preserved {
		b.WriteString(" · raw history preserved")
	}
	return dimStyle.Render(b.String())
}

func (m *model) compactCost(info agent.CompactInfo) (float64, bool) {
	if info.Usage.PromptTokens == 0 && info.Usage.CompletionTokens == 0 {
		return 0, false
	}

	id, _, _ := strings.Cut(info.Model, " @ ")
	return m.usageCost(id, info.Provider, info.Usage)
}

func (m *model) compactRetry() {
	if m.busy {
		m.append(dimStyle.Render("(busy — retry compaction after this turn)"))
		return
	}
	if m.store == nil || m.sessionID == "" {
		m.append(dimStyle.Render("(no session to retry a compaction in)"))
		return
	}
	events := m.store.Compactions(m.sessionID)
	if len(events) == 0 {
		m.append(dimStyle.Render("(no compaction to retry)"))
		return
	}
	last := events[len(events)-1]
	if !m.persist() {
		return
	}
	h, msgs, err := m.store.UndoCompaction(m.sessionID, m.agent.Messages[:1])
	if err != nil {
		m.append(errStyle.Render("/compact retry: " + err.Error()))
		return
	}
	m.history, m.historyID = h, m.sessionID
	m.agent.Messages = msgs
	m.saved = len(msgs)
	m.future = nil
	m.snapshots = m.store.Snapshots(m.sessionID)
	m.rebuildTranscript()
	m.append(dimStyle.Render("⟲ compaction " + strconv.Itoa(last.Seq) + " undone — raw history restored; run /compact to re-compact"))
}

func (m *model) compactLog() {
	if m.store == nil || m.sessionID == "" {
		m.append(dimStyle.Render("(no session)"))
		return
	}
	events := m.store.Compactions(m.sessionID)
	if len(events) == 0 {
		m.append(dimStyle.Render("(no compactions recorded)"))
		return
	}
	var b strings.Builder
	b.WriteString(dimStyle.Render("compactions — raw history preserved; /compact retry undoes the latest:"))
	for _, c := range events {
		summary := strings.Join(strings.Fields(c.Summary), " ")
		if len(summary) > 80 {
			summary = summary[:80] + "…"
		}
		b.WriteString("\n  " + dimStyle.Render("#"+strconv.Itoa(c.Seq)+" folded through message "+strconv.Itoa(c.Cutoff)+": ") + summary)
	}
	m.append(b.String())
}
