package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

func (m *model) lspCommand(fields []string) (tea.Model, tea.Cmd) {
	if m.lspMgr == nil {
		m.append(dimStyle.Render("no LSP servers configured — gopls is built in; add servers via the \"lsp\" block in config.json"))
		return m, nil
	}
	servers := m.lspMgr.Statuses()
	if len(servers) == 0 {
		m.append(dimStyle.Render("no LSP servers"))
		return m, nil
	}
	var b strings.Builder
	b.WriteString("LSP servers:\n")
	for _, s := range servers {
		icon := "○"
		detail := "not started"
		switch s.State {
		case "connected":
			icon = "●"
			detail = "connected"
			if s.Root != "" {
				detail += " (root: " + s.Root + ")"
			}
		case "failed":
			icon = "✗"
			detail = s.Err
		}
		line := fmt.Sprintf("  %s %-16s %s", icon, s.Name, detail)
		switch s.State {
		case "failed":
			b.WriteString(errStyle.Render(line) + "\n")
		case "not started":
			b.WriteString(dimStyle.Render(line) + "\n")
		default:
			b.WriteString(line + "\n")
		}
	}
	m.append(strings.TrimRight(b.String(), "\n"))
	return m, nil
}
