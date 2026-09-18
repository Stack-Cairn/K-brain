package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/mcp"
)

func panelMCPModel(t *testing.T, cfgs, blocked map[string]mcp.ServerConfig) *model {
	t.Helper()
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	m := tasksModel("http://unused")
	m.cfg = &config.Config{}
	if cfgs != nil {
		m.mcpMgr = mcp.NewManager(cfgs)
		m.mcpMgr.SetBlocked(blocked)
		m.mcpMgr.SetOnChange(func() { m.agent.SetMCPTools(m.mcpMgr.Tools()) })
		m.mcpMgr.Start(context.Background())
	}
	return m
}

func TestBuildMCPRows(t *testing.T) {
	off := false
	m := panelMCPModel(t, map[string]mcp.ServerConfig{
		"own": {Command: []string{"true"}, Source: "k-brain"},
	}, map[string]mcp.ServerConfig{
		"gate": {Command: []string{"true"}, Enabled: &off, Note: "blocked by mcpImport config"},
	})
	m.cfg.MCPImport = &config.MCPImport{Codex: &config.MCPImportSource{Enabled: &off}}

	rows := m.buildMCPRows()
	if len(rows) != 4 {
		t.Fatalf("expected 4 rows (2 sources + 1 live + 1 blocked), got %d: %+v", len(rows), rows)
	}
	if !rows[0].source || rows[0].name != "claude" || !rows[0].on {
		t.Errorf("claude row: %+v", rows[0])
	}
	if !rows[1].source || rows[1].name != "codex" || rows[1].on {
		t.Errorf("codex row should be off: %+v", rows[1])
	}
	if rows[2].source || rows[2].name != "own" {
		t.Errorf("server row: %+v", rows[2])
	}
	if !rows[3].disabled || rows[3].name != "gate" {
		t.Errorf("blocked row should be untoggleable: %+v", rows[3])
	}
}

func TestPanelMCPToggleImportOffLive(t *testing.T) {
	dir := t.TempDir()

	codexDir := filepath.Join(dir, ".codex")
	if err := os.MkdirAll(codexDir, 0o700); err != nil {
		t.Fatal(err)
	}
	codexAbs := filepath.Join(codexDir, "config.toml")
	if err := os.WriteFile(codexAbs, []byte("[mcp_servers.fromcodex]\ncommand = [\"true\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	origCodex, origClaude := mcp.CodexPath, mcp.ClaudeGlobalPath
	mcp.CodexPath = func() string { return codexAbs }
	mcp.ClaudeGlobalPath = func() string { return filepath.Join(dir, "absent-claude.json") }
	t.Cleanup(func() { mcp.CodexPath, mcp.ClaudeGlobalPath = origCodex, origClaude })

	m := panelMCPModel(t, map[string]mcp.ServerConfig{
		"fromcodex": {Command: []string{"true"}, Source: codexAbs},
		"own":       {Command: []string{"true"}, Source: "k-brain"},
	}, nil)

	m.openPalette()
	for i, it := range m.palette.items {
		if it.title == "MCPs" {
			m.palette.idx = i
			break
		}
	}
	tm, _ := m.paletteKey(tea.KeyMsg{Type: tea.KeyEnter})
	mdl := tm.(*model)
	pp := mdl.palette.top()
	if pp == nil || pp.kind != panelMCP {
		t.Fatalf("enter should push the MCPs panel, got %+v", pp)
	}
	for pp.mcps[pp.midx].name != "codex" {
		tm, _ = mdl.paletteKey(tea.KeyMsg{Type: tea.KeyDown})
		mdl = tm.(*model)
		pp = mdl.palette.top()
	}
	tm, _ = mdl.paletteKey(tea.KeyMsg{Type: tea.KeyEnter})
	mdl = tm.(*model)

	for _, s := range mdl.mcpMgr.Statuses() {
		if s.Name == "fromcodex" {
			t.Fatalf("toggled-off server still live: %+v", s)
		}
	}
	found := false
	for _, s := range mdl.mcpMgr.Statuses() {
		if s.Name == "own" {
			found = true
		}
	}
	if !found {
		t.Fatal("k-brain-owned server must survive a source toggle")
	}

	var sb strings.Builder
	for _, b := range mdl.blocks {
		sb.WriteString(b.text + "\n")
	}
	joined := sb.String()
	if !strings.Contains(joined, "codex imports: off") || !strings.Contains(joined, "1 server(s) disconnected") {
		t.Fatalf("transcript should report 1 disconnected server, got %q", joined)
	}

	blockedFound := false
	for _, s := range mdl.mcpMgr.Blocked() {
		if s.Name == "fromcodex" {
			blockedFound = true
		}
	}
	if !blockedFound {
		t.Fatal("toggled-off server should appear in the blocked list")
	}

	reloaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.MCPImport == nil || reloaded.MCPImport.Codex == nil ||
		reloaded.MCPImport.Codex.Enabled == nil || *reloaded.MCPImport.Codex.Enabled {
		t.Fatalf("codex import gate should persist as off, got %+v", reloaded.MCPImport)
	}

	pp = mdl.palette.top()
	for _, row := range pp.mcps {
		if row.source && row.name == "codex" && row.on {
			t.Errorf("codex row should render off after the toggle: %+v", row)
		}
	}
}

func TestPaletteMCPRowReplacesServersEntry(t *testing.T) {
	m := compactCmdModel()
	m.openPalette()
	seen := false
	for _, it := range m.palette.all {
		if it.title == "MCP servers" {
			t.Fatal("the MCP servers row should be renamed to MCPs")
		}
		if it.title == "MCPs" {
			seen = true
			if it.panel == nil {
				t.Fatal("the MCPs row must open a panel, not run /mcp")
			}
		}
	}
	if !seen {
		t.Fatal("no MCPs row in the palette")
	}
}

func TestMCPSetImportEnableDiscoversLive(t *testing.T) {

	dir := t.TempDir()
	t.Setenv("K_BRAIN_HOME", dir)
	codexCfg := filepath.Join(dir, "codex.toml")
	if err := os.WriteFile(codexCfg, []byte("[mcp_servers.late]\ncommand = [\"true\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	origCodex, origClaude := mcp.CodexPath, mcp.ClaudeGlobalPath
	mcp.CodexPath = func() string { return codexCfg }
	mcp.ClaudeGlobalPath = func() string { return filepath.Join(dir, "absent-claude.json") }
	t.Cleanup(func() { mcp.CodexPath, mcp.ClaudeGlobalPath = origCodex, origClaude })

	off := false
	m := tasksModel("http://unused")
	m.cfg = &config.Config{MCPImport: &config.MCPImport{Codex: &config.MCPImportSource{Enabled: &off}}}
	m.mcpMgr = mcp.NewManager(nil)
	m.mcpMgr.SetOnChange(func() { m.agent.SetMCPTools(m.mcpMgr.Tools()) })
	m.mcpMgr.Start(context.Background())

	m.mcpSetImport("codex", true)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, s := range m.mcpMgr.Statuses() {
			if s.Name == "late" {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("enabling codex imports never discovered the late server: %+v", m.mcpMgr.Statuses())
}

func TestMCPSetImportKeepsFilters(t *testing.T) {

	dir := t.TempDir()
	origCodex, origClaude := mcp.CodexPath, mcp.ClaudeGlobalPath
	mcp.CodexPath = func() string { return filepath.Join(dir, "absent-codex.toml") }
	mcp.ClaudeGlobalPath = func() string { return filepath.Join(dir, "absent-claude.json") }
	t.Cleanup(func() { mcp.CodexPath, mcp.ClaudeGlobalPath = origCodex, origClaude })

	off := false
	m := panelMCPModel(t, nil, nil)
	m.cfg.MCPImport = &config.MCPImport{Codex: &config.MCPImportSource{Enabled: &off, Exclude: []string{"node_repl"}}}

	m.mcpSetImport("codex", true)

	reloaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	src := reloaded.MCPImport.Codex
	if src.Enabled == nil || !*src.Enabled {
		t.Errorf("enabled should persist, got %+v", src)
	}
	if len(src.Exclude) != 1 || src.Exclude[0] != "node_repl" {
		t.Errorf("exclude filter must survive the toggle, got %v", src.Exclude)
	}

	var sb strings.Builder
	for _, b := range m.blocks[max(0, len(m.blocks)-2):] {
		sb.WriteString(b.text)
	}
	joined := sb.String()
	if !strings.Contains(joined, "codex imports: on") {
		t.Errorf("transcript should note the toggle, got %q", joined)
	}
}
