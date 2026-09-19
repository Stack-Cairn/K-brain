package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/plugins"
	"github.com/Stack-Cairn/K-brain/internal/tools"
	tea "github.com/charmbracelet/bubbletea"
)

func (m *model) pluginCommand(fields []string) (tea.Model, tea.Cmd) {
	if m.pluginMgr == nil {
		m.append(dimStyle.Render("plugins: no plugin directories available"))
		return m, nil
	}
	if len(fields) == 1 || fields[1] == "list" {
		list := m.pluginMgr.List()
		if len(list) == 0 {
			m.append(dimStyle.Render("plugins: none"))
			return m, nil
		}
		var b strings.Builder
		for _, p := range list {
			state := "off"
			if p.Enabled {
				state = "on"
			}
			fmt.Fprintf(&b, "%s  %s  %s\n", state, p.Name, p.Description)
		}
		m.append(dimStyle.Render(strings.TrimSpace(b.String())))
		return m, nil
	}
	if (fields[1] == "reload" && len(fields) != 2) || (fields[1] != "reload" && (len(fields) != 3 || (fields[1] != "enable" && fields[1] != "disable"))) {
		m.append(errStyle.Render("usage: /plugins [list|enable NAME|disable NAME|reload]"))
		return m, nil
	}
	if fields[1] == "reload" {
		if err := m.pluginMgr.Reload(); err != nil {
			m.append(errStyle.Render("plugins: " + err.Error()))
		}
		m.agent.SetPluginTools(pluginToolAdapters(m.pluginMgr))
		m.append(dimStyle.Render("plugins reloaded"))
		return m, nil
	}
	if err := m.pluginMgr.SetEnabled(fields[2], fields[1] == "enable"); err != nil {
		m.append(errStyle.Render(err.Error()))
		return m, nil
	}
	m.agent.SetPluginTools(pluginToolAdapters(m.pluginMgr))
	m.append(dimStyle.Render(fmt.Sprintf("plugin %s %sed", fields[2], fields[1])))
	return m, nil
}

func pluginToolAdapters(m *plugins.Manager) []tools.Tool {
	if m == nil {
		return nil
	}
	var out []tools.Tool
	for _, spec := range m.Tools() {
		name := spec.Name
		desc := spec.Description
		schema := spec.InputSchema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		out = append(out, tools.Tool{
			Def: ai.NewTool(name, desc, string(schema)),
			Run: func(ctx context.Context, args json.RawMessage) (string, error) {
				return m.Invoke(ctx, name, args, nil)
			},
		})
	}
	return out
}
