package tui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/mcp"
)

// The MCP family: tools a configured MCP server provides, named
// mcp__<server>__<tool>. The header reads "server:tool" with the call's
// leading argument as its subject; the output shows up to mcpPreviewLines
// lines before it folds.

// mcpHeaderName is "server:tool" for an MCP tool name.
func mcpHeaderName(name string) (string, bool) {
	srv, tool, ok := mcp.ParseToolName(name)
	if !ok {
		return "", false
	}
	return srv + ":" + tool, true
}

// mcpSubject is an MCP call's subject: its first string argument (by key
// order, so it is stable), else its arguments as compact key=value pairs.
func mcpSubject(args string) string {
	var a map[string]any
	if json.Unmarshal([]byte(args), &a) != nil || len(a) == 0 {
		return ""
	}
	keys := make([]string, 0, len(a))
	for k := range a {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if s, ok := a[k].(string); ok && strings.TrimSpace(s) != "" {
			return truncLine(strings.Join(strings.Fields(s), " "), 80)
		}
	}
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		v, _ := json.Marshal(a[k])
		parts = append(parts, fmt.Sprintf("%s=%s", k, v))
	}
	return truncLine(strings.Join(parts, " "), 80)
}
