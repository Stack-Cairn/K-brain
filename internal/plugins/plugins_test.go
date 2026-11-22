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

func TestLoadValidatesUIPanels(t *testing.T) {
	load := func(manifest string) error {
		d := t.TempDir()
		if err := os.WriteFile(filepath.Join(d, "plugin.json"), []byte(manifest), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := Load(d)
		return err
	}
	panel := func(fields string) string {
		return `{"name":"ui","ui":{"panels":[{` + fields + `}]}}`
	}
	valid := panel(`"id":"main","title":"Main","entry":"pages/main.html","placement":"sidePane"`)
	if err := load(valid); err != nil {
		t.Fatalf("UI-only plugin without a command: %v", err)
	}
	if err := load(`{"name":"ui","ui":{"root":"web","panels":[{"id":"s","title":"Settings","entry":"index.htm","placement":"settings"}]}}`); err != nil {
		t.Fatalf("custom root: %v", err)
	}
	for name, manifest := range map[string]string{
		"tools without command": `{"name":"t","tools":[{"name":"x"}]}`,
		"traversal entry":       panel(`"id":"a","title":"A","entry":"../plugin.json.html","placement":"sidePane"`),
		"absolute entry":        panel(`"id":"a","title":"A","entry":"/etc/a.html","placement":"sidePane"`),
		"dot file entry":        panel(`"id":"a","title":"A","entry":".hidden.html","placement":"sidePane"`),
		"backslash entry":       panel(`"id":"a","title":"A","entry":"a\\b.html","placement":"sidePane"`),
		"script entry":          panel(`"id":"a","title":"A","entry":"a.js","placement":"sidePane"`),
		"unknown placement":     panel(`"id":"a","title":"A","entry":"a.html","placement":"toolbar"`),
		"empty title":           panel(`"id":"a","title":" ","entry":"a.html","placement":"sidePane"`),
		"bad id":                panel(`"id":"../a","title":"A","entry":"a.html","placement":"sidePane"`),
		"escaping root":         `{"name":"ui","ui":{"root":"../outside","panels":[]}}`,
		"duplicate id":          `{"name":"ui","ui":{"panels":[{"id":"a","title":"A","entry":"a.html","placement":"sidePane"},{"id":"a","title":"B","entry":"b.html","placement":"sidePane"}]}}`,
		"unknown panel field":   panel(`"id":"a","title":"A","entry":"a.html","placement":"sidePane","script":"x"`),
	} {
		if err := load(manifest); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
	many := `{"name":"ui","ui":{"panels":[`
	for i := range MaxPanels + 1 {
		if i > 0 {
			many += ","
		}
		many += `{"id":"p` + string(rune('a'+i)) + `","title":"P","entry":"p.html","placement":"sidePane"}`
	}
	if err := load(many + `]}}`); err == nil {
		t.Error("too many panels loaded")
	}
}
