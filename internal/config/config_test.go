package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSaveDefaults(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultModel != "model1" || cfg.Providers["demo"].BaseURL != "" {
		t.Fatalf("defaults: %+v", cfg)
	}

	cfg.DefaultModel = "model2"
	cfg.Experimental = []string{"workflows", "future-thing"}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	cfg2, err := Load()
	if err != nil || cfg2.DefaultModel != "model2" {
		t.Fatalf("reload: %+v %v", cfg2, err)
	}
	if len(cfg2.Experimental) != 2 || cfg2.Experimental[0] != "workflows" {
		t.Fatalf("experimental round-trip: %+v", cfg2.Experimental)
	}

}

func TestLoadRejectsBadJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	os.MkdirAll(filepath.Join(home, ".k-brain"), 0o700)
	os.WriteFile(filepath.Join(home, ".k-brain", "config.json"), []byte("{nope"), 0o600)
	if _, err := Load(); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestProviderKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("K_BRAIN_TEST_KEY", "from-env")

	if k := (Provider{APIKey: "$K_BRAIN_TEST_KEY"}).Key(); k != "from-env" {
		t.Fatalf("env should win: %q", k)
	}
	if k := (Provider{APIKey: "literal"}).Key(); k != "literal" {
		t.Fatalf("literal fallback: %q", k)
	}
	if k := (Provider{BaseURL: "https://other.example.com"}).Key(); k != "" {
		t.Fatalf("no key expected: %q", k)
	}
}

func TestResolveRouting(t *testing.T) {
	cfg := &Config{
		DefaultModel: "m1",
		Providers: map[string]Provider{
			"a": {BaseURL: "https://a", API: "openai-completions"},
			"b": {BaseURL: "https://b", API: "openai-completions"},
		},
		Models: map[string]Model{
			"m1": {Providers: []string{"a", "b"}, ID: "vendor/m1"},
		},
	}
	p, _, id, err := cfg.Resolve("", "")
	if err != nil || p.BaseURL != "https://a" || id != "vendor/m1" {
		t.Fatalf("default routing: %v %v %v", p.BaseURL, id, err)
	}
	p, _, _, err = cfg.Resolve("m1", "b")
	if err != nil || p.BaseURL != "https://b" {
		t.Fatalf("provider override: %v %v", p.BaseURL, err)
	}
	if _, _, _, err = cfg.Resolve("nope", ""); err == nil {
		t.Fatal("expected unknown model error")
	}
	if _, _, _, err = cfg.Resolve("m1", "nope"); err == nil {
		t.Fatal("expected unknown provider error")
	}
}

func TestHomeUnavailable(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	if _, err := Dir(); err == nil {
		t.Fatal("expected Dir error")
	}
	if _, err := Load(); err == nil {
		t.Fatal("expected Load error")
	}
	if err := (&Config{}).Save(); err == nil {
		t.Fatal("expected Save error")
	}
}

func TestLoadJSONCCommentsAndTrailingCommas(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	os.MkdirAll(filepath.Join(home, ".k-brain"), 0o700)
	src := `{
  // default route
  "defaultModel": "m1",
  "defaultProvider": "a", /* block comment */
  "providers": {
    "a": { "baseUrl": "https://a", "api": "openai-completions", "models": [{"id": "m1", "contextWindow": 1024,},], }, // trailing comma
  },
}
`
	os.WriteFile(filepath.Join(home, ".k-brain", "config.json"), []byte(src), 0o600)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultModel != "m1" || cfg.DefaultProvider != "a" {
		t.Fatalf("defaults: %+v", cfg)
	}
	if cfg.Models["m1"].Providers[0] != "a" || cfg.Models["m1"].Context != 1024 {
		t.Fatalf("model: %+v", cfg.Models["m1"])
	}
}

func TestMCPImportRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	os.MkdirAll(filepath.Join(home, ".k-brain"), 0o700)
	src := `{
  "defaultModel": "m1",
  "providers": {
    "a": {
      "baseUrl": "https://a",
      "api": "openai-completions",
      "models": [
        {
          "id": "m1"
        }
      ]
    }
  },
  "mcpImport": {
    "claude": {
      "enabled": false
    },
    "codex": {
      "enabled": true,
      "only": [
        "paper"
      ],
      "exclude": [
        "node_repl"
      ]
    }
  }
}`
	os.WriteFile(filepath.Join(home, ".k-brain", "config.json"), []byte(src), 0o600)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MCPImport == nil || cfg.MCPImport.Claude == nil || cfg.MCPImport.Claude.Enabled == nil || *cfg.MCPImport.Claude.Enabled {
		t.Fatalf("claude should parse as enabled=false, got %+v", cfg.MCPImport)
	}
	if got := cfg.MCPImport.Codex.Exclude; len(got) != 1 || got[0] != "node_repl" {
		t.Fatalf("codex exclude: %+v", cfg.MCPImport.Codex)
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.MCPImport == nil || *reloaded.MCPImport.Claude.Enabled || reloaded.MCPImport.Codex.Only[0] != "paper" {
		t.Fatalf("mcpImport did not round-trip: %+v", reloaded.MCPImport)
	}

	if err := os.WriteFile(filepath.Join(home, ".k-brain", "config.json"), []byte(`{
  "defaultModel": "m1",
  "providers": {
    "a": {
      "baseUrl": "https://a",
      "api": "openai-completions",
      "models": [
        {
          "id": "m1"
        }
      ]
    }
  }
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg2, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg2.MCPImport != nil {
		t.Errorf("absent mcpImport must stay nil, got %+v", cfg2.MCPImport)
	}
}

func TestLoadPreservesMCPImportOnClobber(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	dir := filepath.Join(home, ".k-brain")
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(
		`{
  "providers": null,
  "mcpImport": {
    "codex": {
      "enabled": false
    }
  }
}`,
	), 0o600)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MCPImport == nil || cfg.MCPImport.Codex == nil || *cfg.MCPImport.Codex.Enabled {
		t.Fatalf("mcpImport must survive clobber recovery, got %+v", cfg.MCPImport)
	}
}

func TestLoadRecoversFromClobberedConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	dir := filepath.Join(home, ".k-brain")
	os.MkdirAll(dir, 0o700)
	p := filepath.Join(dir, "config.json")

	os.WriteFile(p, []byte(`{
  "defaultModel": "",
  "providers": null
}`), 0o600)

	os.WriteFile(p+".bak", []byte(`{
  "defaultModel": "m1",
  "providers": {
    "a": {
      "baseUrl": "https://a",
      "api": "openai-completions",
      "models": [
        {
          "id": "m1"
        }
      ]
    }
  }
}`), 0o600)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultModel != "m1" || len(cfg.Providers) != 1 {
		t.Fatalf("expected restore from .bak, got %+v", cfg)
	}
}

func TestLoadRegeneratesDefaultsWhenEmptyAndNoBackup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	dir := filepath.Join(home, ".k-brain")
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{
  "providers": null
}`), 0o600)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultModel != "model1" || len(cfg.Providers) == 0 {
		t.Fatalf("expected regenerated defaults, got %+v", cfg)
	}
}

func TestSaveRefusesToClobberHealthyConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	dir := filepath.Join(home, ".k-brain")
	os.MkdirAll(dir, 0o700)
	p := filepath.Join(dir, "config.json")
	healthy := `{
  "defaultModel": "m1",
  "providers": {
    "a": {
      "baseUrl": "https://a",
      "api": "openai-completions",
      "models": [
        {
          "id": "m1"
        }
      ]
    }
  }
}`
	os.WriteFile(p, []byte(healthy), 0o600)

	if err := (&Config{}).Save(); err == nil {
		t.Fatal("expected refusal to overwrite a healthy config with an empty one")
	}

	data, _ := os.ReadFile(p)
	if string(data) != healthy {
		t.Fatalf("config should be unchanged, got %q", data)
	}
}

func TestSaveWritesBackupAndIsAtomic(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	p, _ := path()
	first, _ := os.ReadFile(p)

	cfg.DefaultModel = "model2"
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	bak, err := os.ReadFile(p + ".bak")
	if err != nil {
		t.Fatal("expected a .bak of the previous contents")
	}
	if string(bak) != string(first) {
		t.Fatalf("backup should hold the previous contents")
	}

	if _, err := os.Stat(p + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("temp file should be renamed away")
	}
}

func TestSaveWritesJSONCHeader(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	p, _ := path()
	data, _ := os.ReadFile(p)
	if len(data) == 0 || data[0] != '/' {
		t.Fatalf("expected a // header comment, got:\n%s", data)
	}

	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
}

func TestLoadCatalogsAlwaysNonNil(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	cats := LoadCatalogs()
	if cats == nil {
		t.Fatal("LoadCatalogs must return a non-nil map so callers can write into it")
	}
	cats["inference"] = Catalog{}
	if len(cats) != 1 {
		t.Fatalf("expected to hold the written entry, got %d", len(cats))
	}
}

func TestLogEventWritesAndRotates(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())

	LogEvent("config.save", "before=(providers=1) after=(providers=1)")
	LogEvent("catalog.fetch", "inference ok: 42 models")
	dir, _ := Dir()
	b, err := os.ReadFile(filepath.Join(dir, "k-brain.log"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, "config.save") || !strings.Contains(s, "catalog.fetch") || !strings.Contains(s, "pid=") {
		t.Fatalf("log content: %q", s)
	}

	os.WriteFile(filepath.Join(dir, "k-brain.log"), make([]byte, logMaxBytes+1), 0o600)
	LogEvent("config.load", "after rotation")
	if _, err := os.Stat(filepath.Join(dir, "k-brain.log.1")); err != nil {
		t.Fatalf("expected rotation: %v", err)
	}
	b, _ = os.ReadFile(filepath.Join(dir, "k-brain.log"))
	if !strings.Contains(string(b), "after rotation") {
		t.Fatalf("fresh log should hold the new event: %q", b)
	}
}

func TestLogEventNeverFails(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", "/nonexistent-\x7f-impossible")
	LogEvent("config.load", "should not panic or error")
}

func TestSetupDoneMarker(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	if SetupDone() {
		t.Fatal("a fresh K_BRAIN_HOME should report setup-not-done")
	}
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	if !Exists() {
		t.Fatal("Load should have written the config")
	}
	if SetupDone() {
		t.Fatal("a subcommand's Load must not mark setup done — the wizard would never run")
	}
	MarkSetupDone()
	if !SetupDone() {
		t.Fatal("the marker should report done")
	}

	dir, _ := Dir()
	if _, err := os.Stat(filepath.Join(dir, "setup.done")); err != nil {
		t.Fatalf("setup.done should exist: %v", err)
	}
}

func TestContextWindow(t *testing.T) {
	if got := (Model{Context: 131072}).ContextWindow(); got != 131072 {
		t.Fatalf("context = %d", got)
	}
}

func TestCatalogMaxCompletionTokens(t *testing.T) {
	c := Catalog{Models: []ModelInfoLite{
		{ID: "a", ContextLength: 1000000, MaxCompletionTokens: 128000},
		{ID: "b", ContextLength: 200000},
	}}
	if got := c.MaxCompletionTokens("a"); got != 128000 {
		t.Fatalf("a: %d", got)
	}
	if got := c.MaxCompletionTokens("b"); got != 0 {
		t.Fatalf("b should be 0 when unadvertised: %d", got)
	}
	if got := c.MaxCompletionTokens("nope"); got != 0 {
		t.Fatalf("unknown: %d", got)
	}
	if got := c.ContextLength("a"); got != 1000000 {
		t.Fatalf("ctx a: %d", got)
	}
}

func TestLoadMixedTokenFields(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	os.MkdirAll(filepath.Join(home, ".k-brain"), 0o700)
	src := `{
  "defaultModel": "m1",
  "providers": {
    "a": {
      "baseUrl": "https://a",
      "api": "openai-completions",
      "models": [
        {
          "id": "m1",
          "contextWindow": 131072
        },
        {
          "id": "m2",
          "contextWindow": 200000,
          "maxTokens": 64000
        }
      ]
    }
  }
}`
	os.WriteFile(filepath.Join(home, ".k-brain", "config.json"), []byte(src), 0o600)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Models["m1"].ContextWindow(); got != 131072 {
		t.Fatalf("m1 context: %d", got)
	}
	m2 := cfg.Models["m2"]
	if m2.ContextWindow() != 200000 || m2.MaxOut != 64000 {
		t.Fatalf("m2: %+v", m2)
	}
}

func TestSnapshotIsolatesMaps(t *testing.T) {
	cfg := Default()
	snap := cfg.Snapshot()
	cfg.Providers["late"] = Provider{BaseURL: "http://late"}
	cfg.Models["late-model"] = Model{Providers: []string{"late"}}
	cfg.DefaultModel = "changed"
	if _, ok := snap.Providers["late"]; ok {
		t.Fatal("snapshot providers must not see later mutations")
	}
	if _, ok := snap.Models["late-model"]; ok {
		t.Fatal("snapshot models must not see later mutations")
	}
	if snap.DefaultModel == "changed" {
		t.Fatal("snapshot scalars are copies")
	}
	if len(snap.Providers) != len(Default().Providers) || len(snap.Models) != len(Default().Models) {
		t.Fatal("snapshot should carry the original entries")
	}
}
