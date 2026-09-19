package tui

import (
	"context"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Stack-Cairn/K-brain/internal/mcp"
)

func TestMCPOnChangeNeverBlocksUI(t *testing.T) {
	m := tasksModel("http://unused")

	p := tea.NewProgram(m, tea.WithoutRenderer())
	defer p.Kill()
	m.prog = p

	m.mcpMgr = mcp.NewManager(nil)
	t.Cleanup(m.mcpMgr.Close)
	m.mcpMgr.SetOnChange(m.mcpOnChange())

	done := make(chan struct{})
	go func() {

		m.mcpMgr.AddServers(context.Background(), map[string]mcp.ServerConfig{
			"docs": {Command: []string{"unused-server"}, Enabled: new(false)},
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the OnChange callback blocked on prog.Send — it must detach the Send (go m.prog.Send)")
	}
}

func TestMCPOnChangeDetachedAfterToggle(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	m := panelMCPModel(t, map[string]mcp.ServerConfig{
		"docs": {Command: []string{"unused-server"}},
	})
	p := tea.NewProgram(m, tea.WithoutRenderer())
	defer p.Kill()
	m.prog = p

	m.mcpMgr.SetOnChange(m.mcpOnChange())
	done := make(chan struct{})
	go func() {
		m.mcpMgr.Disable("docs")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("toggling a server blocked on Send")
	}
}
