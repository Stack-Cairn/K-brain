package agent

import (
	"context"
	"encoding/json"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func newContextTool(a *Agent) tools.Tool {
	return tools.Tool{
		Def:       ai.NewTool("new_context", "Start a fresh context window without summarizing conversation history. Use this when the context window is nearly full and the task can continue from the workspace state.", `{"type":"object","properties":{}}`),
		NoInherit: true,
		Run: func(context.Context, json.RawMessage) (string, error) {
			a.requestNewContext()
			return "A new context window will start without summarizing conversation history.", nil
		},
	}
}
