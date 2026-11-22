package plugins

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestToolNamesParseBackToOnePlugin(t *testing.T) {
	for _, tc := range []struct{ plugin, tool string }{
		{"notes", "add"},
		{"my-notes", "add_item"},
		{"notes.v2", "add"},
		{"my_notes", "add"},
		{"my_notes", "add_item"},
		{"a_b_c", "x_y"},
	} {
		name := ToolName(tc.plugin, tc.tool)
		key, tool, ok := ParseToolName(name)
		if !ok || key != ToolKey(tc.plugin) || tool != tc.tool || strings.Contains(key, "_") {
			t.Errorf("%s/%s: name %q parses to %q %q %v", tc.plugin, tc.tool, name, key, tool, ok)
		}
	}
	// Names of plugins without "_" are unchanged.
	if got := ToolName("notes", "add"); got != "plugin_notes_add" {
		t.Fatalf("stable name = %q", got)
	}
	if got := ToolName("notes.v2", "add_item"); got != "plugin_notes.v2_add_item" {
		t.Fatalf("stable name = %q", got)
	}
	// "_" and "-" do not collide.
	if ToolKey("my_notes") == ToolKey("my-notes") || !strings.HasPrefix(ToolKey("my_notes"), "my-notes-") {
		t.Fatalf("keys %q %q", ToolKey("my_notes"), ToolKey("my-notes"))
	}
	for _, bad := range []string{"plugin_", "plugin_x", "plugin__x", "plugin_x_", "mcp__x__y", "notes_add"} {
		if _, _, ok := ParseToolName(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestInvokeRunsToolsOfPluginsWithUnderscores(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fixture plugin is a shell script")
	}
	home := isolatePluginHome(t)
	for _, name := range []string{"my_notes", "my"} {
		dir := filepath.Join(home, "plugins", name)
		writeManifest(t, dir, `{"name":"`+name+`","command":["./run.sh"],"enabled":true,"tools":[{"name":"add_item"},{"name":"notes_add"}]}`)
		// The script echoes the plugin and the tool name it was asked for.
		script := "#!/bin/sh\nread line\nname=$(printf '%s' \"$line\" | sed 's/.*\"name\":\"\\([^\"]*\\)\".*/\\1/')\nprintf '{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"content\":\"" + name + ":%s\"}}\\n' \"$name\"\n"
		if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	m, err := New("")
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range m.Tools() {
		names[tool.Name] = true
	}
	for _, want := range []string{ToolName("my_notes", "add_item"), "plugin_my_add_item", "plugin_my_notes_add"} {
		if !names[want] {
			t.Fatalf("tools %v lack %q", names, want)
		}
	}
	for name, want := range map[string]string{
		ToolName("my_notes", "add_item"):  "my_notes:add_item",
		ToolName("my_notes", "notes_add"): "my_notes:notes_add",
		"plugin_my_add_item":              "my:add_item",
		// Before keys, this name was read as plugin "my", tool "notes_add".
		"plugin_my_notes_add": "my:notes_add",
	} {
		out, err := m.Invoke(context.Background(), name, json.RawMessage(`{}`), nil)
		if err != nil || out != want {
			t.Errorf("Invoke(%q) = %q, %v; want %q", name, out, err, want)
		}
	}
	if _, err := m.Invoke(context.Background(), "plugin_missing_add", nil, nil); err == nil {
		t.Fatal("unknown plugin invoked")
	}
}

func TestLoadIgnoresUnknownManifestFields(t *testing.T) {
	d := t.TempDir()
	manifest := `{"name":"demo","command":["demo"],"homepage":"https://example.invalid","engines":{"k-brain":">=2"},
		"tools":[{"name":"echo","annotations":{"readOnly":true}}],
		"ui":{"icon":"x.svg","panels":[{"id":"main","title":"Main","entry":"index.html","placement":"sidePane","minWidth":200}]}}`
	if err := os.WriteFile(filepath.Join(d, "plugin.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := Load(d)
	if err != nil || len(p.Tools) != 1 || p.UI == nil || len(p.UI.Panels) != 1 {
		t.Fatalf("plugin = %+v, %v", p, err)
	}
	// Known fields are still validated.
	if err := os.WriteFile(filepath.Join(d, "plugin.json"), []byte(`{"name":"demo","extra":1,"tools":[{"name":"echo"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(d); err == nil {
		t.Fatal("tools without a command loaded")
	}
}
