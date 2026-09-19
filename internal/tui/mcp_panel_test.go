package tui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/mcp"
)

func panelMCPModel(t *testing.T, cfgs map[string]mcp.ServerConfig) *model {
	t.Helper()
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	m := tasksModel("http://unused")
	m.cfg = config.Default()
	if cfgs != nil {
		m.mcpMgr = mcp.NewManager(cfgs)
		t.Cleanup(m.mcpMgr.Close)
	}
	return m
}

func openMCPPanel(t *testing.T, m *model) *ppanel {
	t.Helper()
	m.openPalette()
	for i, it := range m.palette.items {
		if it.title == "MCPs" {
			m.palette.idx = i
			m.paletteKey(tea.KeyMsg{Type: tea.KeyEnter})
			pp := m.palette.top()
			if pp == nil || pp.kind != panelMCP {
				t.Fatalf("missing MCP panel: %+v", pp)
			}
			return pp
		}
	}
	t.Fatal("missing MCPs menu entry")
	return nil
}

func waitMCPStatus(t *testing.T, m *model, want mcp.Status) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		statuses := m.mcpMgr.Statuses()
		if len(statuses) == 1 && statuses[0].Status == want {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("wanted %s, got %+v", want, statuses)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestBuildMCPRows(t *testing.T) {
	off := false
	m := panelMCPModel(t, map[string]mcp.ServerConfig{
		"own":     {Command: []string{"unused-server"}, Source: "k-brain"},
		"zzz-off": {Command: []string{"unused-server"}, Enabled: &off},
	})
	rows := m.buildMCPRows()
	if len(rows) != 2 || rows[0].name != "own" || !rows[0].on ||
		rows[1].name != "zzz-off" || rows[1].on {
		t.Fatalf("expected configured enabled and disabled servers, got %+v", rows)
	}
	if rows := panelMCPModel(t, nil).buildMCPRows(); len(rows) != 0 {
		t.Fatalf("unconfigured panel should be empty: %+v", rows)
	}
}

func TestPanelMCPToggleNativeServer(t *testing.T) {
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "docs", Version: "test"}, nil)
	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name: "ping", InputSchema: map[string]any{"type": "object"},
	}, func(context.Context, *sdkmcp.CallToolRequest, struct{}) (*sdkmcp.CallToolResult, any, error) {
		return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "pong"}}}, nil, nil
	})
	httpServer := httptest.NewServer(sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server {
		return server
	}, nil))
	t.Cleanup(httpServer.Close)
	m := panelMCPModel(t, map[string]mcp.ServerConfig{
		"docs": {
			URL: httpServer.URL, Headers: map[string]string{"X-Test": "value"},
			StartupTimeout: 3, ToolTimeout: 7, Note: "local documentation",
		},
	})
	m.mcpMgr.Start(t.Context())
	waitMCPStatus(t, m, mcp.StatusReady)
	assertTool := func() {
		t.Helper()
		tools := m.mcpMgr.Tools()
		if len(tools) != 1 {
			t.Fatalf("expected one live MCP tool, got %d", len(tools))
		}
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		out, err := tools[0].Run(ctx, nil)
		if err != nil || out != "pong" {
			t.Fatalf("MCP tool call failed: %q, %v", out, err)
		}
	}
	assertTool()
	pp := openMCPPanel(t, m)
	m.paletteKey(tea.KeyMsg{Type: tea.KeyEnter})
	waitMCPStatus(t, m, mcp.StatusDisabled)
	if len(m.mcpMgr.Tools()) != 0 {
		t.Fatal("disabled server still exposes tools")
	}
	if len(pp.mcps) != 1 || pp.mcps[0].on {
		t.Fatalf("server row still enabled: %+v", pp.mcps)
	}
	saved, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	entry := saved.MCPServers["docs"]
	if entry.Enabled == nil || *entry.Enabled || entry.URL != httpServer.URL ||
		entry.Headers["X-Test"] != "value" || entry.StartupTimeout != 3 ||
		entry.ToolTimeout != 7 || entry.Note != "local documentation" {
		t.Fatalf("server definition lost while persisting toggle: %+v", entry)
	}
	m.paletteKey(tea.KeyMsg{Type: tea.KeyEnter})
	if !pp.mcps[0].on {
		t.Fatal("enabled checkbox must update before reconnect completes")
	}
	waitMCPStatus(t, m, mcp.StatusReady)
	assertTool()
	saved, err = config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if saved.MCPServers["docs"].Enabled == nil || !*saved.MCPServers["docs"].Enabled {
		t.Fatal("enable did not persist")
	}
}

func TestMCPToggleSaveFailureKeepsState(t *testing.T) {
	m := panelMCPModel(t, map[string]mcp.ServerConfig{
		"docs": {Command: []string{"unused-server"}},
	})
	m.cfg.MCPServers = map[string]config.MCPServer{
		"docs": {Command: []string{"unused-server"}, Enabled: new(true)},
	}
	before := m.cfg.MCPServers["docs"]
	home, err := config.Dir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(home, "config.json.tmp"), 0700); err != nil {
		t.Fatal(err)
	}
	pp := openMCPPanel(t, m)
	m.paletteKey(tea.KeyMsg{Type: tea.KeyEnter})
	if !reflect.DeepEqual(m.cfg.MCPServers["docs"], before) || !pp.mcps[0].on {
		t.Fatal("failed save changed in-memory config or panel")
	}
	live, _ := m.mcpMgr.Config("docs")
	if live.Disabled() {
		t.Fatal("failed save disabled live server")
	}
	if !strings.Contains(m.blocks[len(m.blocks)-1].text, "config save failed") {
		t.Fatal("save failure was not reported")
	}
}

func TestPanelMCPRemovedServers(t *testing.T) {
	for _, key := range []tea.KeyType{tea.KeyUp, tea.KeyDown, tea.KeyTab, tea.KeyShiftTab, tea.KeyEnter, tea.KeyLeft, tea.KeyRight} {
		t.Run(key.String(), func(t *testing.T) {
			m := panelMCPModel(t, map[string]mcp.ServerConfig{
				"docs": {Command: []string{"unused-server"}},
			})
			pp := openMCPPanel(t, m)
			m.mcpMgr.RemoveServers("docs")
			m.Update(mcpStatusMsg{})
			if len(pp.mcps) != 0 || pp.midx != 0 {
				t.Fatalf("status update left an invalid selection: %+v", pp)
			}
			m.paletteKey(tea.KeyMsg{Type: key})
			if len(pp.mcps) != 0 || pp.midx != 0 {
				t.Fatalf("stale panel after removal: %+v", pp)
			}
			m.paletteKey(tea.KeyMsg{Type: tea.KeyEsc})
			if m.palette.top() != nil {
				t.Fatal("empty panel cannot be closed")
			}
		})
	}
}

func TestPanelMCPRefreshPreservesSelection(t *testing.T) {
	m := panelMCPModel(t, map[string]mcp.ServerConfig{
		"a": {Command: []string{"unused-server"}, Enabled: new(false)},
		"b": {Command: []string{"unused-server"}, Enabled: new(false)},
	})
	pp := openMCPPanel(t, m)
	m.paletteKey(tea.KeyMsg{Type: tea.KeyDown})
	if pp.mcps[pp.midx].name != "b" {
		t.Fatal("fixture failed to select server b")
	}
	m.mcpMgr.RemoveServers("a")
	m.Update(mcpStatusMsg{})
	if pp.midx != 0 || pp.mcps[pp.midx].name != "b" {
		t.Fatal("removal lost selection")
	}
	m.mcpMgr.AddServers(t.Context(), map[string]mcp.ServerConfig{
		"a": {Command: []string{"unused-server"}, Enabled: new(false)},
	})
	m.Update(mcpStatusMsg{})
	if pp.midx != 1 || pp.mcps[pp.midx].name != "b" {
		t.Fatal("insertion changed the selected server")
	}
}

func TestPaletteMCPHasNoImportControls(t *testing.T) {
	m := compactCmdModel()
	m.openPalette()
	for _, it := range m.palette.all {
		if it.title == "MCPs" {
			if it.panel == nil {
				t.Fatal("MCPs must open a panel")
			}
			desc := strings.ToLower(it.dynDesc(m))
			for _, removed := range []string{"import", "claude", "codex"} {
				if strings.Contains(desc, removed) {
					t.Fatalf("obsolete import control: %s", desc)
				}
			}
			return
		}
	}
	t.Fatal("no MCPs menu entry")
}
