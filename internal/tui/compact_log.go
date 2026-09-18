package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
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
	if m.store != nil && m.sessionID != "" {
		b.WriteString(" · raw history preserved")
	}
	return dimStyle.Render(b.String())
}

func (m *model) compactCost(info agent.CompactInfo) (float64, bool) {
	if info.Usage.PromptTokens == 0 && info.Usage.CompletionTokens == 0 {
		return 0, false
	}

	id, _, _ := strings.Cut(info.Model, " @ ")
	for _, cat := range m.catalogs {
		if in, out, cacheRead, ok := cat.Pricing(id); ok {
			return ai.SessionCost(info.Usage, in, out, cacheRead), true
		}
	}
	return 0, false
}

func (m *model) rawCutoff(cutoff int) int {
	if m.store == nil || m.sessionID == "" {
		return cutoff
	}
	events := m.store.Compactions(m.sessionID)
	if len(events) == 0 {
		return cutoff
	}

	return events[len(events)-1].Cutoff + cutoff - 1
}

func (m *model) compactRetry() {
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
	if err := m.store.DeleteCompaction(m.sessionID, last.Seq); err != nil {
		m.append(errStyle.Render("/compact retry: " + err.Error()))
		return
	}
	m.append(dimStyle.Render("⟲ compaction " + strconv.Itoa(last.Seq) + " undone — raw history restored; run /compact to re-compact"))

	_, msgs, err := m.store.Load(m.sessionID)
	if err != nil {
		m.append(errStyle.Render("/compact retry: reload failed: " + err.Error()))
		return
	}
	m.agent.Messages = append(m.agent.Messages[:1], msgs[1:]...)
	m.saved = 1
	m.rebuildTranscript()
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
