package tui

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/agent"
)

// The plan pane: the agent's todowrite list, pinned above the composer while
// it has open items (and for the rest of a turn that updated it, so the last
// tick is seen). The todowrite calls themselves stay out of the transcript;
// the pane is where the plan lives.
//
//	Plan  2/5
//	✓ Read the config loader
//	◐ Wire the plan pane
//	○ Add tests
//	⋯ 2 more
//
// On a short terminal it shrinks to one row naming the current step.

// todoPaneMaxItems is how many items the pane lists at a terminal height: 5
// from 30 rows, 3 from 20, else none (the one-row form).
func todoPaneMaxItems(height int) int {
	switch {
	case height >= 30:
		return 5
	case height >= 20:
		return 3
	}
	return 0
}

// onTodos takes the plan the running agent just recorded.
func (m *model) onTodos(msg todosMsg) {
	if msg.agent != m.agent {
		return
	}
	tv := m.tvs()
	tv.todos, tv.todoAgent = msg.items, msg.agent
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

// todoPaneView is the pane, or "" while it is hidden.
func (m *model) todoPaneView() string {
	items := m.todoItems()
	if len(items) == 0 {
		return ""
	}
	open := slices.IndexFunc(items, todoOpen)
	if open < 0 && !m.busy {
		return ""
	}
	done := 0
	for _, it := range items {
		if it.Status == "completed" || it.Status == "cancelled" {
			done++
		}
	}
	width := max(m.width, 1)
	head := toolNameStyle.Render("Plan") + textMutedStyle.Render(fmt.Sprintf("  %d/%d", done, len(items)))
	limit := todoPaneMaxItems(m.height)
	if limit == 0 {
		cur := slices.IndexFunc(items, func(t agent.Todo) bool { return t.Status == "in_progress" })
		if cur < 0 {
			cur = open
		}
		if cur >= 0 {
			head += textMutedStyle.Render(chromeGlyphs(" · ")) + textSecondaryStyle.Render(todoText(items[cur].Content))
		}
		return truncCells(head, width)
	}
	// The window starts one before the first open item, so the step just
	// finished stays in view above the current one.
	start := 0
	if open > 0 {
		start = open - 1
	}
	if len(items)-start < limit {
		start = max(len(items)-limit, 0)
	}
	end := min(start+limit, len(items))
	out := []string{truncCells(head, width)}
	for _, it := range items[start:end] {
		out = append(out, truncCells(todoRow(it), width))
	}
	if rest := len(items) - end; rest > 0 {
		out = append(out, textMutedStyle.Render(fmt.Sprintf("%s %d more", glyphs.Fold, rest)))
	}
	return strings.Join(out, "\n")
}

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

// todoPaneSection is the pane as viewBody draws it: set off by a blank row.
func (m *model) todoPaneSection() string {
	if v := m.todoPaneView(); v != "" {
		return "\n" + v + "\n"
	}
	return ""
}

// todoPaneRows is the rows the pane takes, its separator included.
func (m *model) todoPaneRows() int {
	if v := m.todoPaneView(); v != "" {
		return strings.Count(v, "\n") + 2
	}
	return 0
}

func todoText(s string) string { return strings.Join(strings.Fields(s), " ") }
