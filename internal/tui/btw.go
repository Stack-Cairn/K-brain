package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/sandbox"
	tea "github.com/charmbracelet/bubbletea"
)

func btwMessages(messages []ai.Message, question string) []ai.Message {
	start := 0
	if len(messages) > 13 {
		start = len(messages) - 13
	}
	out := make([]ai.Message, 0, len(messages)-start+1)
	for _, msg := range messages[start:] {
		if msg.Role == "tool" || msg.Role == "function" {
			continue
		}
		clone := ai.Message{Role: msg.Role, Content: msg.Content}
		if clone.Content != "" {
			out = append(out, clone)
		}
	}
	out = append(out, ai.Message{Role: "user", Content: "Answer this side question without changing the main task or assuming the answer will be sent as a new turn: " + question})
	return out
}

func (m *model) btwCommand(question string) tea.Cmd {
	question = strings.TrimSpace(question)
	if question == "" {
		m.append(errStyle.Render("usage: /btw <question>"))
		return nil
	}
	if m.busy || m.btwBusy {
		m.append(dimStyle.Render("(busy — /btw after this turn)"))
		return nil
	}
	if m.agent == nil || m.agent.Client == nil {
		m.append(errStyle.Render("/btw: no API client configured"))
		return nil
	}
	m.btwBusy = true
	m.append(dimStyle.Render("◎ asking a side question…"))
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	ctx = sandbox.WithPolicy(ctx, m.sandboxPolicy)
	run := func() tea.Msg {
		result, usage, err := m.agent.Client.Complete(ctx, ai.Request{
			Model:     m.agent.Model,
			MaxTokens: 4096,
			Messages:  btwMessages(m.agent.Messages, question),
		})
		m.agent.AddUsage(usage)
		cancel()
		return btwMsg{result: result, err: err}
	}
	if m.prog == nil {
		msg := run()
		m.Update(msg)
		return nil
	}
	return run
}

func (m *model) btwLabel() string {
	if m.btwBusy {
		return fmt.Sprintf("◎ %s", m.tr("side question"))
	}
	return ""
}
