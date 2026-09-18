package tui

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/mcp"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func mcpModel(t *testing.T, cfgs map[string]mcp.ServerConfig) *model {
	t.Helper()
	m := tasksModel("http://unused")
	m.cfg = &config.Config{}
	if cfgs != nil {
		m.mcpMgr = mcp.NewManager(cfgs)
	}
	return m
}

func TestMCPCommandNoServers(t *testing.T) {
	m := mcpModel(t, nil)
	m.command("/mcp")
	last := m.blocks[len(m.blocks)-1].text
	if !strings.Contains(last, "no MCP servers configured") {
		t.Errorf("got %q", last)
	}
}

func TestMCPStatusView(t *testing.T) {
	disabled := false
	m := mcpModel(t, map[string]mcp.ServerConfig{
		"broken":  {Command: []string{"/nonexistent-binary-xyz"}},
		"off":     {Command: []string{"true"}, Enabled: &disabled, Note: "turned off"},
		"invalid": {},
	})
	m.command("/mcp")
	out := m.blocks[len(m.blocks)-1].text
	for _, want := range []string{"broken", "off", "invalid", "disabled"} {
		if !strings.Contains(out, want) {
			t.Errorf("status view missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "invalid config") {
		t.Errorf("invalid entry should explain itself:\n%s", out)
	}
}

func TestMCPLiveServerEndToEnd(t *testing.T) {

	m := mcpModel(t, map[string]mcp.ServerConfig{"docs": {Command: []string{"docs"}}})
	m.mcpMgr.SetOnChange(func() {})
	m.command("/mcp docs reconnect")
	last := m.blocks[len(m.blocks)-1].text
	if !strings.Contains(last, "reconnecting") {
		t.Errorf("got %q", last)
	}
	m.command("/mcp nope reconnect")
	last = m.blocks[len(m.blocks)-1].text
	if !strings.Contains(last, "no MCP server named nope") {
		t.Errorf("got %q", last)
	}
}

func TestMCPTogglePersists(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	m := mcpModel(t, map[string]mcp.ServerConfig{"docs": {Command: []string{"docs"}}})
	m.command("/mcp docs disable")
	entry, ok := m.cfg.MCPServers["docs"]
	if !ok || entry.Enabled == nil || *entry.Enabled {
		t.Fatalf("disable should persist enabled=false, got %+v", entry)
	}

	reloaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.MCPServers["docs"].Enabled == nil || *reloaded.MCPServers["docs"].Enabled {
		t.Error("persisted disable did not round-trip")
	}
	if len(reloaded.MCPServers["docs"].Command) == 0 {
		t.Error("disable must copy the full server definition, not a bare enabled flag")
	}
}

func TestMCPSurvivesAgentSwap(t *testing.T) {
	m := mcpModel(t, map[string]mcp.ServerConfig{"docs": {Command: []string{"docs"}}})
	mcpT := tools.Tool{Def: ai.NewTool("mcp__docs__greet", "g", `{"type":"object"}`)}

	m.mcpMgr.SetOnChange(func() { m.agent.SetMCPTools([]tools.Tool{mcpT}) })
	m.agent.SetMCPTools([]tools.Tool{mcpT})

	old := m.agent
	m.agent = agent.New(old.Client, old.Model, old.MaxTokens, "sys")
	m.mcpMgr.FireOnChangeForTest()
	if !agHasTool(m.agent, "mcp__docs__greet") {
		t.Fatal("post-swap OnChange must write to the new agent")
	}
	if agHasTool(old, "mcp__docs__greet") != true {
		t.Fatal("old agent is untouched after swap (its set was already pushed)")
	}
}

func TestMCPBlockedViewAndEnableGuard(t *testing.T) {
	m := mcpModel(t, map[string]mcp.ServerConfig{"docs": {Command: []string{"docs"}}})
	off := false
	m.mcpMgr.SetBlocked(map[string]mcp.ServerConfig{
		"node_repl": {Command: []string{"/app/bin/node_repl"}, Enabled: &off, Note: "blocked by mcpImport config"},
	})
	m.command("/mcp")
	out := m.blocks[len(m.blocks)-1].text
	if !strings.Contains(out, "node_repl") || !strings.Contains(out, "blocked by mcpImport config") {
		t.Errorf("blocked server must stay visible with its note:\n%s", out)
	}
	m.command("/mcp node_repl enable")
	last := m.blocks[len(m.blocks)-1].text
	if !strings.Contains(last, "blocked by the mcpImport config") || !strings.Contains(last, "config.json") {
		t.Errorf("enable on a blocked server should point at the config, got %q", last)
	}
	if _, written := m.cfg.MCPServers["node_repl"]; written {
		t.Error("enabling a blocked server must not write a config entry")
	}
}

func agHasTool(a *agent.Agent, name string) bool {
	for _, t := range a.AllTools() {
		if t.Def.Function.Name == name {
			return true
		}
	}
	return false
}

var (
	_ = sdkmcp.Tool{}
	_ = context.Background
)

func TestMCPFirstSettleNote(t *testing.T) {
	disabled := false
	m := mcpModel(t, map[string]mcp.ServerConfig{
		"dead": {Command: []string{"nope-not-a-binary-xyz"}, StartupTimeout: 2, Source: "/proj/.mcp.json"},
		"off":  {Command: []string{"true"}, Enabled: &disabled},
	})

	var mu sync.Mutex
	m.mcpMgr.SetOnChange(func() {
		mu.Lock()
		defer mu.Unlock()
		m.Update(mcpStatusMsg{})
	})
	m.mcpMgr.Start(context.Background())

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		done := 0
		for _, s := range m.mcpMgr.Statuses() {
			if s.Status != mcp.StatusConnecting {
				done++
			}
		}
		if done == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	var sb strings.Builder
	for _, b := range m.blocks {
		sb.WriteString(b.text)
		sb.WriteByte('\n')
	}
	text := sb.String()
	mu.Unlock()
	if !strings.Contains(text, "mcp: dead failed") || !strings.Contains(text, "/mcp dead reconnect") {
		t.Errorf("missing failure note:\n%s", text)
	}
	if !strings.Contains(text, "(/proj/.mcp.json)") {
		t.Errorf("failure note should name the config file:\n%s", text)
	}
	if !strings.Contains(text, "mcp: off disabled") {
		t.Errorf("missing disabled note:\n%s", text)
	}

	mu.Lock()
	before := len(m.blocks)
	mu.Unlock()
	m.mcpMgr.FireOnChangeForTest()
	mu.Lock()
	if len(m.blocks) != before {
		t.Error("second settle must not re-announce")
	}
	mu.Unlock()
}
