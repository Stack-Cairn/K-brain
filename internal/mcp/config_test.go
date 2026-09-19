package mcp

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

func TestFromConfigMapOwnsMutableFields(t *testing.T) {
	input := map[string]config.MCPServer{
		"stdio":  {Command: []string{"server", "--stdio"}, Env: map[string]string{"KEY": "$LOCAL_KEY"}, Cwd: "workspace", Enabled: new(false), Note: "local", StartupTimeout: 4, ToolTimeout: 9},
		"remote": {URL: "https://example.test/mcp", Headers: map[string]string{"Authorization": "$LOCAL_TOKEN"}},
	}
	servers := FromConfigMap(input)
	want := map[string]ServerConfig{
		"stdio":  {Command: []string{"server", "--stdio"}, Env: map[string]string{"KEY": "$LOCAL_KEY"}, Cwd: "workspace", Enabled: new(false), Note: "local", StartupTimeout: 4, ToolTimeout: 9, Source: "k-brain"},
		"remote": {URL: "https://example.test/mcp", Headers: map[string]string{"Authorization": "$LOCAL_TOKEN"}, Source: "k-brain"},
	}
	if !reflect.DeepEqual(servers, want) {
		t.Fatalf("conversion lost configuration: %+v", servers)
	}
	servers["stdio"].Command[0] = "changed"
	servers["stdio"].Env["KEY"] = "changed"
	*servers["stdio"].Enabled = true
	servers["remote"].Headers["Authorization"] = "changed"
	delete(servers, "remote")
	if !reflect.DeepEqual(FromConfigMap(input), want) {
		t.Fatal("runtime configuration changes leaked into saved configuration")
	}
	for _, empty := range []map[string]config.MCPServer{nil, {}} {
		if FromConfigMap(empty) != nil {
			t.Fatal("empty configuration must not discover servers")
		}
	}
}

func TestManagerStatusSource(t *testing.T) {
	mgr := NewManager(FromConfigMap(map[string]config.MCPServer{
		"docs": {Command: []string{"unused-server"}, Enabled: new(false)},
	}))
	t.Cleanup(mgr.Close)
	statuses := mgr.Statuses()
	if len(statuses) != 1 || statuses[0].Name != "docs" || statuses[0].Source != "k-brain" || statuses[0].Status != StatusDisabled {
		t.Fatalf("configured server status: %+v", statuses)
	}
}

func TestToolNameRoundTrip(t *testing.T) {

	name := ToolName("my-server", "get_doc.v2")
	if name != "mcp__my-server__get_doc_v2" {
		t.Errorf("ToolName = %q", name)
	}
	srv, tool, ok := ParseToolName(name)
	if !ok || srv != "my-server" || tool != "get_doc_v2" {
		t.Fatalf("ParseToolName(%q) = %q %q %v", name, srv, tool, ok)
	}

	name = ToolName("my_server", "do_thing_now")
	srv, tool, ok = ParseToolName(name)
	if !ok || srv != "my_server" || tool != "do_thing_now" {
		t.Fatalf("ParseToolName(%q) = %q %q %v", name, srv, tool, ok)
	}

	if ToolName("a.b", "t") == ToolName("a b", "t") {
		t.Error("sanitized names must not collide")
	}
	srv, _, ok = ParseToolName(ToolName("a.b", "t"))
	if !ok || !strings.HasPrefix(srv, "a-b_") {
		t.Errorf("hashed server key should be unambiguous, got %q", srv)
	}
	if _, _, ok := ParseToolName("bash"); ok {
		t.Error("bash is not an MCP tool")
	}
	if _, _, ok := ParseToolName("mcp__broken"); ok {
		t.Error("mcp__ without server__tool split is invalid")
	}
}

func TestValidAndDefaults(t *testing.T) {
	if (ServerConfig{}).Valid() == "" {
		t.Error("empty config should be invalid")
	}
	c := ServerConfig{Command: []string{"x"}}
	if c.StartupTimeoutDuration() != 30*time.Second || c.ToolTimeoutDuration() != 60*time.Second {
		t.Error("default timeouts wrong")
	}
	if (ServerConfig{URL: "ftp://x"}).Valid() == "" {
		t.Error("non-http url should be invalid")
	}
	if (ServerConfig{URL: "http://x", Command: []string{"y"}}).Valid() == "" {
		t.Error("command+url should be invalid")
	}
}
