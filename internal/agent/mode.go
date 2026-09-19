package agent

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

const planInstruction = "Plan mode is active. Inspect existing files with read, ask clarifying questions, and use todowrite to develop a concrete implementation plan. Do not execute commands, modify files, operate browsers or desktops, launch tasks, or call external tools. Present the plan for review; implementation requires the user to leave Plan mode."

func (a *Agent) SetPlanMode(enabled bool) { a.planMode.Store(enabled) }
func (a *Agent) PlanMode() bool           { return a.planMode != nil && a.planMode.Load() }

func planToolAllowed(name string) bool {
	switch name {
	case "read", "question", "todowrite":
		return true
	default:
		return false
	}
}

func (a *Agent) modeTools(all []tools.Tool) []tools.Tool {
	out := make([]tools.Tool, 0, len(all))
	for _, tool := range all {
		if planToolAllowed(tool.Def.Function.Name) {
			out = append(out, tool)
			continue
		}
		if a.PlanMode() {
			continue
		}
		run := tool.Run
		tool.Run = func(ctx context.Context, args json.RawMessage) (string, error) {
			if a.PlanMode() {
				return "", errors.New("Plan mode blocks this tool; switch modes before implementation")
			}
			return run(ctx, args)
		}
		out = append(out, tool)
	}
	return out
}

func (a *Agent) modeMessages(messages []ai.Message) []ai.Message {
	if !a.PlanMode() {
		return messages
	}
	out := append([]ai.Message(nil), messages...)
	for i := range out {
		if out[i].Role == "system" {
			out[i].Content += "\n\n" + planInstruction
			return out
		}
	}
	return append([]ai.Message{{Role: "system", Content: planInstruction}}, out...)
}
