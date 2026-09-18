package tui

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

type titleMsg struct {
	sessionID     string
	previousTitle string
	title         string
}

func (m *model) maybeTitle() tea.Cmd {
	if m.store == nil || m.sessionID == "" || m.titled {
		return nil
	}
	meta, _, err := m.store.Load(m.sessionID)
	if err != nil || meta.Title == "" {
		return nil
	}
	m.titled = true
	cli, mdl := m.agent.CompactClient, m.agent.CompactModel
	if cli == nil {
		cli = m.agent.Client
	}
	if cli == nil {
		return nil
	}
	if mdl == "" {
		mdl = m.agent.Model
	}
	var userTxt, asstTxt string
	for _, msg := range m.agent.Messages {
		if userTxt == "" && msg.Role == "user" {
			userTxt = msg.TextContent()
		} else if msg.Role == "assistant" {
			asstTxt = msg.TextContent()
		}
		if userTxt != "" && asstTxt != "" {
			break
		}
	}
	if userTxt == "" {
		return nil
	}
	p := m.prog
	sessionID, previousTitle := m.sessionID, meta.Title
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		out, _, err := cli.Complete(ctx, ai.Request{
			Model:     mdl,
			MaxTokens: 24,
			Messages: []ai.Message{
				{Role: "system", Content: "You name chat sessions. Reply with a short title (3-6 words, plain text, no quotes, no trailing period) summarizing the user's request."},
				{Role: "user", Content: "Request: " + truncLine(userTxt, 300) + "\nResponse: " + truncLine(asstTxt, 200)},
			},
		})
		if err != nil {
			return
		}
		title := strings.Trim(strings.TrimSpace(out), "\"'.")
		if title == "" || len(title) > 80 {
			return
		}
		if p != nil {
			p.Send(titleMsg{sessionID: sessionID, previousTitle: previousTitle, title: title})
		}
	}()
	return nil
}
