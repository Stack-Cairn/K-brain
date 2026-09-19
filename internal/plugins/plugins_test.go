package plugins

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadManifestAndTools(t *testing.T) {
	d := t.TempDir()
	os.WriteFile(filepath.Join(d, "plugin.json"), []byte(`{"name":"demo","version":"1","enabled":true,"command":["demo"],"prompt":"use demo","tools":[{"name":"echo","description":"echo"}]}`), 0o600)
	p, err := Load(d)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "demo" || !p.Enabled || len(p.Tools) != 1 {
		t.Fatalf("unexpected plugin: %+v", p)
	}
}

func TestLoadRejectsTraversalName(t *testing.T) {
	d := t.TempDir()
	os.WriteFile(filepath.Join(d, "plugin.json"), []byte(`{"name":"../bad","command":["x"]}`), 0o600)
	if _, err := Load(d); err == nil {
		t.Fatal("expected invalid name")
	}
}
