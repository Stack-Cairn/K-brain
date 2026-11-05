package tui

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Stack-Cairn/K-brain/internal/mcp"
	"github.com/Stack-Cairn/K-brain/internal/skills"
)

// The extensions modal lists what extends the agent: MCP servers, skills,
// hooks and plugins, one tab each. The lists are read-only; the only actions
// are the ones the slash commands already offer (enable, disable, reconnect,
// reload).

func (m *model) extensionsTabs() []surfaceTab {
	// Skills are scanned from disk once per opening, not on every keystroke.
	var skillRows []surfaceRow
	scanned := false
	skillTab := func(m *model) []surfaceRow {
		if !scanned {
			skillRows, scanned = skillSurfaceRows(m), true
		}
		return skillRows
	}
	return []surfaceTab{
		{name: "MCP", rows: mcpSurfaceRows, empty: "no MCP servers configured — add one with `kn mcp add <name> -- <cmd...>`",
			hints: "↑/↓ select · Enter enable/disable · Ctrl+R reconnect · Esc close"},
		{name: "Skills", rows: skillTab, empty: "no skills found",
			hints: "↑/↓ select · Esc close"},
		{name: "Hooks", rows: hookSurfaceRows, empty: "no hooks configured",
			hints: "↑/↓ select · Esc close"},
		{name: "Plugins", rows: pluginSurfaceRows, empty: "no plugins installed",
			hints: "↑/↓ select · Enter enable/disable · Ctrl+R reload · Esc close"},
	}
}

// extensionsCommand is /extensions [mcp|skills|hooks|plugins].
func (m *model) extensionsCommand(args []string) {
	tabs := m.extensionsTabs()
	tab := 0
	if len(args) > 0 {
		for i, t := range tabs {
			if strings.EqualFold(t.name, args[0]) {
				tab = i
			}
		}
	}
	if m.height <= 0 {
		var b strings.Builder
		for _, t := range tabs {
			rows := t.rows(m)
			fmt.Fprintf(&b, "%s (%d)\n", t.name, len(rows))
			for _, r := range rows {
				line := "  " + r.key
				if r.value != "" {
					line += "  " + r.value
				}
				if r.desc != "" {
					line += "  " + r.desc
				}
				b.WriteString(line + "\n")
			}
		}
		m.append(dimStyle.Render(strings.TrimRight(b.String(), "\n")))
		return
	}
	m.openSurface(&surfaceModal{kind: "extensions", title: "Extensions", tabs: tabs, tab: tab})
}

func mcpSurfaceRows(m *model) []surfaceRow {
	if m.mcpMgr == nil {
		return nil
	}
	var rows []surfaceRow
	for _, s := range m.mcpMgr.Statuses() {
		cfg, ok := m.mcpMgr.Config(s.Name)
		if !ok {
			continue
		}
		s, on := s, !cfg.Disabled()
		value := s.Status.String()
		switch s.Status {
		case mcp.StatusReady:
			value = fmt.Sprintf("ready · %d tools", s.Tools)
		case mcp.StatusFailed:
			value = "failed"
		}
		desc := s.Note
		if desc == "" {
			desc = strings.Join(cfg.Command, " ")
		}
		if desc == "" {
			desc = cfg.URL
		}
		name := s.Name
		rows = append(rows, surfaceRow{
			key:    name,
			desc:   desc,
			value:  value,
			muted:  !on,
			search: s.Source,
			preview: func(m *model) []string {
				out := []string{cardTitleStyle.Render(name) + "  " + textHintStyle.Render(chromeGlyphs(value))}
				if cfg.URL != "" {
					out = append(out, textSecondaryStyle.Render("url: "+cfg.URL))
				}
				if len(cfg.Command) > 0 {
					out = append(out, textSecondaryStyle.Render("command: "+strings.Join(cfg.Command, " ")))
				}
				if s.Source != "" {
					out = append(out, textMutedStyle.Render("source: "+s.Source))
				}
				if s.Err != "" {
					w, _ := m.surfaceSize()
					for _, l := range wrapPreview(s.Err, w) {
						out = append(out, errStyle.Render(l))
					}
				}
				return out
			},
			enter: func(m *model) tea.Cmd {
				if err := m.mcpPersistEnabled(name, !on); err != nil {
					m.app.modal.note = err.Error()
					return nil
				}
				state := "disabled"
				if !on {
					state = "enabled"
				}
				m.app.modal.note = "mcp server " + name + ": " + state + " (persisted)"
				return nil
			},
			alt: func(m *model) {
				if m.mcpMgr.Reconnect(name) {
					m.app.modal.note = "reconnecting mcp server " + name + "…"
				} else {
					m.app.modal.note = "no MCP server named " + name
				}
			},
		})
	}
	return rows
}

func skillSurfaceRows(m *model) []surfaceRow {
	var list []skills.Skill
	if m.skillScan != nil {
		list = m.skillScan()
	}
	var rows []surfaceRow
	for _, s := range list {
		s := s
		value := ""
		if s.DisableModelInvocation {
			value = "manual"
		}
		if s.Warning != "" {
			value = "warning"
		}
		rows = append(rows, surfaceRow{
			key:    s.Name,
			desc:   s.Description,
			value:  value,
			search: s.Path,
			preview: func(m *model) []string {
				w, _ := m.surfaceSize()
				out := []string{cardTitleStyle.Render(s.Name)}
				for _, l := range wrapPreview(s.Description, w) {
					out = append(out, textSecondaryStyle.Render(l))
				}
				if s.DisableModelInvocation {
					out = append(out, textHintStyle.Render("invoked only by name, never by the model"))
				}
				if s.Warning != "" {
					out = append(out, warnStyle.Render(s.Warning))
				}
				out = append(out, textMutedStyle.Render(s.Path))
				return out
			},
		})
	}
	return rows
}

func hookSurfaceRows(m *model) []surfaceRow {
	var rows []surfaceRow
	if m.cfg != nil {
		events := make([]string, 0, len(m.cfg.Hooks))
		for e := range m.cfg.Hooks {
			events = append(events, e)
		}
		sort.Strings(events)
		for _, e := range events {
			for _, h := range m.cfg.Hooks[e] {
				h, e := h, e
				value := ""
				if h.Timeout > 0 {
					value = fmt.Sprintf("%ds", h.Timeout)
				}
				rows = append(rows, surfaceRow{
					section: "config",
					key:     e,
					desc:    h.Command,
					value:   value,
					search:  h.Shell,
					preview: func(m *model) []string {
						w, _ := m.surfaceSize()
						out := []string{cardTitleStyle.Render(e)}
						for _, l := range wrapPreview(h.Command, w) {
							out = append(out, textSecondaryStyle.Render(l))
						}
						if h.Shell != "" {
							out = append(out, textMutedStyle.Render("shell: "+h.Shell))
						}
						return out
					},
				})
			}
		}
	}
	if m.pluginMgr != nil {
		for _, p := range m.pluginMgr.List() {
			events := make([]string, 0, len(p.Hooks))
			for e := range p.Hooks {
				events = append(events, e)
			}
			sort.Strings(events)
			for _, e := range events {
				cmd, e, name := p.Hooks[e], e, p.Name
				rows = append(rows, surfaceRow{
					section: "plugin " + name,
					key:     e,
					desc:    cmd,
					muted:   !p.Enabled,
					preview: func(m *model) []string {
						return []string{cardTitleStyle.Render(e), textSecondaryStyle.Render(cmd), textMutedStyle.Render("plugin: " + name)}
					},
				})
			}
		}
	}
	return rows
}

func pluginSurfaceRows(m *model) []surfaceRow {
	if m.pluginMgr == nil {
		return nil
	}
	var rows []surfaceRow
	for _, p := range m.pluginMgr.List() {
		p := p
		value := onOff(p.Enabled)
		if p.Err != nil {
			value = "error"
		}
		if p.Version != "" {
			value = p.Version + " · " + value
		}
		var toolNames []string
		for _, t := range p.Tools {
			toolNames = append(toolNames, t.Name)
		}
		rows = append(rows, surfaceRow{
			key:    p.Name,
			desc:   p.Description,
			value:  value,
			muted:  !p.Enabled,
			search: strings.Join(toolNames, " "),
			preview: func(m *model) []string {
				w, _ := m.surfaceSize()
				out := []string{cardTitleStyle.Render(p.Name) + "  " + textHintStyle.Render(value)}
				for _, l := range wrapPreview(p.Description, w) {
					out = append(out, textSecondaryStyle.Render(l))
				}
				if len(toolNames) > 0 {
					out = append(out, textSecondaryStyle.Render("tools: "+strings.Join(toolNames, ", ")))
				}
				if len(p.Hooks) > 0 {
					events := make([]string, 0, len(p.Hooks))
					for e := range p.Hooks {
						events = append(events, e)
					}
					slices.Sort(events)
					out = append(out, textSecondaryStyle.Render("hooks: "+strings.Join(events, ", ")))
				}
				if p.Err != nil {
					out = append(out, errStyle.Render(p.Err.Error()))
				}
				out = append(out, textMutedStyle.Render(p.Dir))
				return out
			},
			enter: func(m *model) tea.Cmd {
				if err := m.pluginMgr.SetEnabled(p.Name, !p.Enabled); err != nil {
					m.app.modal.note = err.Error()
					return nil
				}
				m.agent.SetPluginTools(pluginToolAdapters(m.pluginMgr))
				state := "enabled"
				if p.Enabled {
					state = "disabled"
				}
				m.app.modal.note = "plugin " + p.Name + " " + state
				return nil
			},
			alt: func(m *model) {
				if err := m.pluginMgr.Reload(); err != nil {
					m.app.modal.note = "plugins: " + err.Error()
				} else {
					m.app.modal.note = "plugins reloaded"
				}
				m.agent.SetPluginTools(pluginToolAdapters(m.pluginMgr))
			},
		})
	}
	return rows
}
