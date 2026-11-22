package plugins

import (
	"os"
	"path/filepath"
	"testing"
)

func writeManifest(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func isolatePluginHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("LIVEAGENT_HOME", home)
	t.Setenv("K_BRAIN_HOME", "")
	return home
}

func TestInstallTargetsTheScannedUserDirectory(t *testing.T) {
	home := isolatePluginHome(t)
	src := filepath.Join(t.TempDir(), "src")
	writeManifest(t, src, `{"name":"demo","version":"1","command":["demo"]}`)
	m, err := New("")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Install(src); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "plugins", "demo", "plugin.json")); err != nil {
		t.Fatalf("installed plugin not under the user plugin directory: %v", err)
	}
	p, ok := m.Get("demo")
	if !ok || m.Source(p) != "user" {
		t.Fatalf("installed plugin not listed as a user plugin: %+v %v", p, ok)
	}
	if err := m.Install(src); err == nil {
		t.Fatal("second install of the same plugin succeeded")
	}
}

func TestReloadRereadsStateSavedByAnotherManager(t *testing.T) {
	isolatePluginHome(t)
	project := t.TempDir()
	writeManifest(t, filepath.Join(project, ".liveagent", "plugins", "probe"), `{"name":"probe","command":["x"],"enabled":true}`)
	writer, err := New(project)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := New(project)
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := reader.Get("probe"); !p.Enabled || reader.Source(p) != "workspace" {
		t.Fatalf("workspace plugin = %+v source %q", p, reader.Source(p))
	}
	if err := writer.SetEnabled("probe", false); err != nil {
		t.Fatal(err)
	}
	if err := reader.Reload(); err != nil {
		t.Fatal(err)
	}
	if p, _ := reader.Get("probe"); p.Enabled {
		t.Fatal("reload kept the stale enabled state")
	}
	// A save from the stale manager must not drop the other plugin's state.
	writeManifest(t, filepath.Join(project, ".liveagent", "plugins", "other"), `{"name":"other","command":["x"],"enabled":true}`)
	if err := reader.Reload(); err != nil {
		t.Fatal(err)
	}
	if err := writer.Reload(); err != nil {
		t.Fatal(err)
	}
	if err := reader.SetEnabled("other", false); err != nil {
		t.Fatal(err)
	}
	if err := writer.SetEnabled("probe", true); err != nil {
		t.Fatal(err)
	}
	if err := reader.Reload(); err != nil {
		t.Fatal(err)
	}
	if p, _ := reader.Get("other"); p.Enabled {
		t.Fatal("a stale manager's save overwrote another plugin's state")
	}
	if p, _ := reader.Get("probe"); !p.Enabled {
		t.Fatal("probe not re-enabled")
	}
}
