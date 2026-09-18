package tui

import (
	"context"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/mcp"
)

func TestMCPOnChangeNeverBlocksUI(t *testing.T) {
	m := tasksModel("http://unused")

	p := tea.NewProgram(m, tea.WithoutRenderer())
	defer p.Kill()
	m.prog = p

	m.mcpMgr = mcp.NewManager(nil)
	m.mcpMgr.SetOnChange(m.mcpOnChange())

	done := make(chan struct{})
	go func() {

		m.mcpMgr.AddServers(context.Background(), map[string]mcp.ServerConfig{
			"docs": {Command: []string{"true"}},
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the OnChange callback blocked on prog.Send — it must detach the Send (go m.prog.Send)")
	}
}

func TestMCPLazyManagerOnChangeDetached(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	m := tasksModel("http://unused")
	m.cfg = &config.Config{}
	p := tea.NewProgram(m, tea.WithoutRenderer())
	defer p.Kill()
	m.prog = p

	m.mcpSetImport("claude", true)
	if m.mcpMgr == nil {
		t.Fatal("mcpSetImport should have built a manager")
	}
	done := make(chan struct{})
	go func() {
		m.mcpMgr.FireOnChangeForTest()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the lazily-built manager's OnChange callback blocked on Send — it must detach")
	}
}
