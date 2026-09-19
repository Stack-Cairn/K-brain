package tui

import (
	"fmt"
	"maps"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/mcp"
)

func (m *model) mcpCommand(fields []string) (tea.Model, tea.Cmd) {
	if m.mcpMgr == nil {
		m.append(dimStyle.Render("no MCP servers configured — add one with `kn mcp add <name> -- <cmd...>`"))
		return m, nil
	}
	if len(fields) == 1 {
		m.append(m.mcpStatusView())
		return m, nil
	}
	name := fields[1]
	action := "reconnect"
	if len(fields) > 2 {
		action = fields[2]
	}
	switch action {
	case "reconnect":
		if !m.mcpMgr.Reconnect(name) {
			m.append(errStyle.Render("no MCP server named " + name))
			return m, nil
		}
		m.append(dimStyle.Render(fmt.Sprintf("↻ reconnecting mcp server %s…", name)))
	case "disable", "enable":
		m.mcpSetEnabled(name, action == "enable")
	default:
		m.append(errStyle.Render("usage: /mcp [name] [reconnect|enable|disable]"))
	}
	return m, nil
}

func (m *model) mcpSetEnabled(name string, enabled bool) {
	if m.mcpMgr == nil {
		m.append(errStyle.Render("no MCP server named " + name))
		return
	}
	live, ok := m.mcpMgr.Config(name)
	if !ok {
		m.append(errStyle.Render("no MCP server named " + name))
		return
	}
	entry := config.MCPServer{
		Command: live.Command, Env: live.Env, Cwd: live.Cwd,
		URL: live.URL, Headers: live.Headers,
		StartupTimeout: live.StartupTimeout, ToolTimeout: live.ToolTimeout,
		Note:    live.Note,
		Enabled: &enabled,
	}
	next := config.Config{}
	if m.cfg != nil {
		next = *m.cfg
	}
	next.MCPServers = maps.Clone(next.MCPServers)
	if next.MCPServers == nil {
		next.MCPServers = map[string]config.MCPServer{}
	}
	next.MCPServers[name] = entry
	if err := next.Save(); err != nil {
		m.append(errStyle.Render("config save failed: " + err.Error()))
		return
	}
	if m.cfg == nil {
		m.cfg = &next
	} else {
		m.cfg.MCPServers = next.MCPServers
	}
	if enabled {
		m.mcpMgr.Enable(name)
	} else {
		m.mcpMgr.Disable(name)
	}
	state := "disabled"
	if enabled {
		state = "enabled"
	}
	m.append(dimStyle.Render(fmt.Sprintf("mcp server %s: %s (persisted)", name, state)))
	m.append(m.mcpStatusView())
}

func (m *model) mcpOnChange() func() {
	return func() {
		m.agent.SetMCPTools(m.mcpMgr.Tools())
		if m.prog != nil {
			go m.prog.Send(mcpStatusMsg{})
		}
	}
}

func (m *model) buildMCPRows() []mcpRow {
	var rows []mcpRow
	if m.mcpMgr == nil {
		return rows
	}
	for _, s := range m.mcpMgr.Statuses() {
		cfg, exists := m.mcpMgr.Config(s.Name)
		if !exists {
			continue
		}
		detail := s.Status.String()
		switch s.Status {
		case mcp.StatusReady:
			detail = fmt.Sprintf("ready · %d tools", s.Tools)
		case mcp.StatusFailed:
			detail = "failed — " + s.Err
		}
		rows = append(rows, mcpRow{name: s.Name, on: !cfg.Disabled(), detail: detail})
	}
	return rows
}

func (m *model) refreshMCPPanel(pp *ppanel) {
	selected := ""
	if pp.midx >= 0 && pp.midx < len(pp.mcps) {
		selected = pp.mcps[pp.midx].name
	}
	pp.mcps = m.buildMCPRows()
	pp.midx = max(0, min(pp.midx, len(pp.mcps)-1))
	for i, row := range pp.mcps {
		if row.name == selected {
			pp.midx = i
			break
		}
	}
}

func (m *model) mcpStatusView() string {
	servers := m.mcpMgr.Statuses()
	if len(servers) == 0 {
		return dimStyle.Render("no MCP servers")
	}
	var b strings.Builder
	b.WriteString("MCP servers:\n")
	for _, s := range servers {
		icon := "◌"
		detail := ""
		switch s.Status {
		case mcp.StatusReady:
			icon = "●"
			detail = fmt.Sprintf("%d tools", s.Tools)
		case mcp.StatusFailed:
			icon = "✗"
			detail = s.Err
			if s.Source != "" {
				detail += " (" + s.Source + ")"
			}
		case mcp.StatusDisabled:
			icon = "○"
			detail = "disabled"
			if s.Note != "" {
				detail = "disabled — " + s.Note
			}
		case mcp.StatusConnecting:
			icon = "◌"
			detail = "connecting…"
		}
		line := fmt.Sprintf("  %s %-20s %s", icon, s.Name, detail)
		switch s.Status {
		case mcp.StatusReady:
			b.WriteString(line + "\n")
		case mcp.StatusFailed:
			b.WriteString(errStyle.Render(line) + "\n")
		default:
			b.WriteString(dimStyle.Render(line) + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
