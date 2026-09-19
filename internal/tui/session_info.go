package tui

import (
	"fmt"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/agent"
)

func (m *model) sessionInfo() string {
	id := m.sessionID
	if id == "" {
		id = "(unsaved)"
	}
	title := strings.TrimSpace(m.sessTitle)
	if title == "" {
		title = "(untitled)"
	}
	model := m.modelName
	provider := m.provName
	effort := "off"
	if m.agent != nil {
		if m.agent.Model != "" {
			model = m.agent.Model
		}
		effort = effortLabel(m.agent.Effort)
	}
	usage := "0/0 tok"
	messages := 0
	context := ""
	if m.agent != nil {
		u := m.agent.TotalUsage()
		usage = fmtUsage(u)
		messages = max(len(m.agent.Messages)-1, 0)
		if m.agent.ContextLimit > 0 {
			context = fmt.Sprintf("\ncontext: %d%% of %s", agent.EstimateTokens(m.agent.Messages)*100/m.agent.ContextLimit, fmtTok(m.agent.ContextLimit))
		}
	}
	if m.store != nil && m.sessionID != "" {
		if path := m.store.TranscriptPath(m.sessionID); path != "" {
			context += "\nsession file: " + path
		}
	}
	return dimStyle.Render(fmt.Sprintf("session\nid: %s\ntitle: %s\nmodel: %s\nprovider: %s\neffort: %s\nmessages: %d\nusage: %s\nworking directory: %s%s", id, title, model, provider, effort, messages, usage, cwd(), context))
}

func (m *model) planInfo() string {
	if m.agent == nil || len(m.agent.Todos) == 0 {
		return dimStyle.Render("plan\n(no active plan — the model can create one with todowrite)")
	}
	var b strings.Builder
	b.WriteString("plan")
	for _, item := range m.agent.Todos {
		mark := " "
		switch item.Status {
		case "completed":
			mark = "✓"
		case "in_progress":
			mark = "→"
		case "cancelled":
			mark = "×"
		default:
			mark = "·"
		}
		fmt.Fprintf(&b, "\n%s %s", mark, item.Content)
	}
	return dimStyle.Render(b.String())
}
