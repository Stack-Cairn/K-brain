package tui

import (
	"slices"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/agent"
)

// The plan: the agent's todowrite list. The todowrite calls stay out of the
// transcript; the plan lives in the top panel (top_panel.go) as a "☰ Plan d/n"
// chip, with its steps listed while the panel is expanded. Only a plan the
// agent wrote in this run shows: a resumed session's list stays in /plan, and
// a finished one goes away when the user starts the next turn.

// onTodos takes the plan the running agent just recorded.
func (m *model) onTodos(msg todosMsg) {
	if msg.agent != m.agent {
		return
	}
	tv := m.tvs()
	tv.todos, tv.todoAgent = msg.items, msg.agent
	m.panelPlanReported()
	m.layout()
}

// todoItems is the plan to show. Between turns the agent is idle and its list
// is read directly, which also picks up a resumed session's plan and drops a
// replaced agent's; during a turn only what the agent reported counts.
func (m *model) todoItems() []agent.Todo {
	tv := m.tvs()
	if !m.busy && m.agent != nil {
		tv.todos, tv.todoAgent = slices.Clone(m.agent.Todos), m.agent
	}
	if tv.todoAgent != m.agent {
		return nil
	}
	return tv.todos
}

func todoOpen(t agent.Todo) bool { return t.Status == "pending" || t.Status == "in_progress" }

// todoRow is one plan item with its status mark.
func todoRow(t agent.Todo) string {
	text := todoText(t.Content)
	switch t.Status {
	case "completed":
		return okStyle.Render(todoMark(t.Status)) + " " + textMutedStyle.Render(text)
	case "in_progress":
		return runningStyle.Bold(true).Render(todoMark(t.Status)) + " " + toolNameStyle.Render(text)
	case "cancelled":
		return textMutedStyle.Render(todoMark(t.Status) + " " + text)
	}
	return textMutedStyle.Render(todoMark(t.Status)) + " " + textSecondaryStyle.Render(text)
}

func todoMark(status string) string {
	if asciiMode {
		switch status {
		case "completed":
			return "[x]"
		case "in_progress":
			return "[>]"
		case "cancelled":
			return "[-]"
		}
		return "[ ]"
	}
	switch status {
	case "completed":
		return glyphs.OK
	case "in_progress":
		return "◐"
	case "cancelled":
		return glyphs.Fail
	}
	return "○"
}

func todoText(s string) string { return strings.Join(strings.Fields(s), " ") }
