package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

const (
	maxTodos       = 50
	maxTodoContent = 300
)

type Todo struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	Status  string `json:"status"`
}

var todoStatuses = map[string]bool{
	"pending": true, "in_progress": true, "completed": true, "cancelled": true,
}

func (a *Agent) setTodos(items []Todo) (int, error) {
	if len(items) > maxTodos {
		return 0, fmt.Errorf("list exceeds %d items; consolidate steps", maxTodos)
	}
	seen := map[string]bool{}
	open, inProgress := 0, 0
	for i, it := range items {
		it.Content = strings.TrimSpace(it.Content)
		if it.Content == "" || len(it.Content) > maxTodoContent {
			return 0, fmt.Errorf("todo %d needs non-empty content of at most %d chars", i+1, maxTodoContent)
		}
		if !todoStatuses[it.Status] {
			return 0, fmt.Errorf("todo %d has invalid status %q (pending|in_progress|completed|cancelled)", i+1, it.Status)
		}
		if it.ID == "" {
			it.ID = fmt.Sprintf("t%d", i+1)
		}
		if seen[it.ID] {
			return 0, fmt.Errorf("duplicate todo id %q", it.ID)
		}
		seen[it.ID] = true
		switch it.Status {
		case "in_progress":
			inProgress++
			open++
		case "pending":
			open++
		}
		items[i] = it
	}
	if inProgress > 1 {
		return 0, fmt.Errorf("keep exactly one item in_progress (%d given)", inProgress)
	}
	a.Todos = items
	a.todosMu.Lock()
	fn := a.onTodos
	a.todosMu.Unlock()
	if fn != nil {
		fn(append([]Todo(nil), items...))
	}
	return open, nil
}

func (a *Agent) todoBlock() string {
	var b strings.Builder
	for _, it := range a.Todos {
		if it.Status == "pending" || it.Status == "in_progress" {
			fmt.Fprintf(&b, "- [%s] %s: %s\n", it.Status, it.ID, it.Content)
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return "Your current plan (from todowrite). Keep it updated: rewrite the full list each call, keep one item in_progress, mark items completed only once verified.\n\n" + strings.TrimRight(b.String(), "\n")
}

func (a *Agent) TodosJSON() string {
	if len(a.Todos) == 0 {
		return ""
	}
	b, err := json.Marshal(a.Todos)
	if err != nil {
		return ""
	}
	return string(b)
}

func (a *Agent) LoadTodosJSON(s string) {
	var items []Todo
	if s != "" && json.Unmarshal([]byte(s), &items) == nil {
		a.Todos = items
	}
}

func todoTool(a *Agent) tools.Tool {
	return tools.Tool{
		Def: ai.NewTool("todowrite",
			"Record and update your plan for this conversation. Rewrite the FULL list on every call — the list you send replaces the previous one and open items are shown back to you each round. Use it for any task needing 3 or more steps; skip it for trivial one-step work. Keep exactly one item in_progress and mark items completed only after verifying they are actually done. Send an empty list to clear it.",
			`{"type":"object","properties":{"todos":{"type":"array","description":"The full, updated plan.","items":{"type":"object","properties":{"id":{"type":"string","description":"Stable id, e.g. t1 (assigned if omitted)"},"content":{"type":"string","description":"The step, phrased as an imperative"},"status":{"type":"string","enum":["pending","in_progress","completed","cancelled"]}},"required":["content","status"]}}},"required":["todos"]}`),
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				Todos []Todo `json:"todos"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", err
			}
			open, err := a.setTodos(in.Todos)
			if err != nil {
				return "", err
			}
			if len(in.Todos) == 0 {
				return "Plan cleared.", nil
			}
			return fmt.Sprintf("Plan updated: %d item(s), %d open.", len(in.Todos), open), nil
		},
	}
}
