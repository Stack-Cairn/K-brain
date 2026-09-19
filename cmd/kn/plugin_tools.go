package main

import (
	"context"
	"encoding/json"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/plugins"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func pluginTools(pm *plugins.Manager) []tools.Tool {
	if pm == nil {
		return nil
	}
	var out []tools.Tool
	for _, spec := range pm.Tools() {
		name := spec.Name
		schema := spec.InputSchema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		out = append(out, tools.Tool{Def: ai.NewTool(name, spec.Description, string(schema)), Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			return pm.Invoke(ctx, name, args, nil)
		}})
	}
	return out
}
