package tui

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/hooks"
	"github.com/Stack-Cairn/K-brain/internal/plugins"
	"github.com/Stack-Cairn/K-brain/internal/sandbox"
)

func TestModelChangesPreserveSessionLifecycle(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	m := compactCmdModel()
	m.sessionID = "active-session"
	m.sandboxPolicy = &sandbox.Policy{Mode: "off", Root: t.TempDir()}
	m.agent.SetSessionID(m.sessionID)
	starts := 0
	m.agent.PluginHook = func(context.Context, hooks.Event) error { starts++; return nil }
	if err := m.agent.StartSession(t.Context()); err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	dir := filepath.Join(project, ".k-brain", "plugins", "probe")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"name":"probe","enabled":true,"command":["unused"],"hooks":{"SessionStart":"exit 9"},"tools":[{"name":"read","description":"test"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	var err error
	m.pluginMgr, err = plugins.New(project)
	if err != nil {
		t.Fatal(err)
	}
	m.cfg.Hooks = map[string][]config.Hook{"SessionStart": {{Command: "exit 9"}}}
	m.wireTasks()
	for _, preview := range []bool{true, false} {
		if preview {
			m.previewModel(modelItem{model: config.DefaultCompactModel, provider: m.provName})
		} else {
			m.switchModel("glm-5.2-fast", m.provName, false)
		}
		if m.agent.SessionIDValue() != m.sessionID || m.agent.SandboxPolicy != m.sandboxPolicy || m.agent.PluginHook == nil {
			t.Fatal("model change lost runtime context")
		}
		found := false
		for _, tool := range m.agent.AllTools() {
			if tool.Def.Function.Name == "plugin_probe_read" {
				found = true
			}
		}
		if !found {
			t.Fatal("model change lost plugin tools")
		}
		if err := m.agent.StartSession(t.Context()); err != nil {
			t.Fatalf("model change repeated startup: %v", err)
		}
	}
	if starts != 1 {
		t.Fatalf("start count = %d", starts)
	}
}
