package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestMCPServersRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("K_BRAIN_HOME", home)
	cfg := Default()
	cfg.MCPServers = map[string]MCPServer{
		"stdio":  {Command: []string{"server", "--stdio"}, Cwd: "工作目录", Env: map[string]string{"TOKEN": "$MCP_TOKEN"}, Enabled: new(false), Note: "local server", StartupTimeout: 5, ToolTimeout: 12},
		"remote": {URL: "https://example.test/mcp", Headers: map[string]string{"Authorization": "$MCP_TOKEN"}},
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reloaded.MCPServers, cfg.MCPServers) {
		t.Fatalf("MCP configuration did not round-trip: %+v", reloaded.MCPServers)
	}
	path := filepath.Join(home, "config.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, []byte(`"mcpImport"`)) {
		t.Fatal("saved configuration contains removed import settings")
	}
}

func TestLoadRejectsRemovedMCPImport(t *testing.T) {
	home := t.TempDir()
	t.Setenv("K_BRAIN_HOME", home)
	body, err := marshalConfig(Default())
	if err != nil {
		t.Fatal(err)
	}
	body, err = stripJSONC(body)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	document["mcpImport"] = json.RawMessage(`{"codex":{"enabled":true},"claude":{"enabled":true}}`)
	body, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "config.json")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), `unknown field "mcpImport"`) {
		t.Fatalf("removed configuration field must be rejected: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, body) {
		t.Fatalf("failed load changed configuration: %v", err)
	}
}

func TestLoadPreservesMCPServersOnClobber(t *testing.T) {
	for _, backup := range []bool{false, true} {
		t.Run(map[bool]string{false: "defaults", true: "backup"}[backup], func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("K_BRAIN_HOME", home)
			path := filepath.Join(home, "config.json")
			if backup {
				body, err := marshalConfig(Default())
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path+".bak", body, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(path, []byte(`{"providers":null,"mcp":{"docs":{"command":["server"],"enabled":false}}}`), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]MCPServer{"docs": {Command: []string{"server"}, Enabled: new(false)}}
			if !reflect.DeepEqual(cfg.MCPServers, want) || len(cfg.Providers) == 0 {
				t.Fatalf("recovery lost configuration: %+v", cfg)
			}
			reloaded, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(reloaded.MCPServers, want) {
				t.Fatal("recovery did not persist MCP servers")
			}
		})
	}
}
