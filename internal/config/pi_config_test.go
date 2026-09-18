package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPiConfigRejectsLegacyFields(t *testing.T) {
	for _, input := range []string{
		`{"providers":{},"models":{}}`,
		`{"providers":{},"uiMode":"default"}`,
		`{"providers":{"p":{"apiKeyEnv":"API_KEY"}}}`,
		`{"providers":{"p":{"auth":"subscription"}}}`,
		`{"providers":{"p":{"api":"openai-codex-responses"}}}`,
		`{"name":"p","models":{"model1":{}}}`,
		`{"name":"p","models":[{}]}`,
		`{"name":"p","models":[{"id":"model1","contextWindow":-1}]}`,
		`{"name":"p","models":[{"id":"model1","maxTokens":-1}]}`,
		`null`,
	} {
		var cfg Config
		if err := parseConfigJSONC([]byte(input), &cfg); err == nil {
			t.Errorf("accepted invalid or legacy config: %s", input)
		}
	}
}

func TestPiDefaultsRoundTrip(t *testing.T) {
	data, err := marshalConfig(Default())
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]json.RawMessage
	if err := parseJSONC(data, &root); err != nil {
		t.Fatal(err)
	}
	if _, ok := root["models"]; ok {
		t.Fatal("default config must not have legacy top-level models")
	}
	var cfg Config
	if err := parseConfigJSONC(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Models) != 2 || cfg.DefaultModel != "model1" || cfg.CompactModel != "model1" {
		t.Fatalf("defaults = %+v", cfg)
	}
	for _, id := range []string{"model1", "model2"} {
		if cfg.Models[id].Context != 128000 || cfg.Models[id].MaxOut != 8192 {
			t.Errorf("default limits for %s = %+v", id, cfg.Models[id])
		}
		if _, _, got, err := cfg.Resolve(id, ""); err != nil || got != id {
			t.Errorf("resolve %s = %q, %v", id, got, err)
		}
	}
}

func TestPiProviderConfigShape(t *testing.T) {
	home := t.TempDir()
	t.Setenv("K_BRAIN_HOME", home)
	input := `{
  "name": "local",
  "api": "openai-completions",
  "models": [{"id": "demo", "name": "Demo", "contextWindow": 8192, "maxTokens": 512}],
  "baseUrl": "http://127.0.0.1:1234/v1",
  "apiKey": "test-key"
}`
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	p, ok := cfg.Providers["local"]
	if !ok || p.BaseURL != "http://127.0.0.1:1234/v1" || p.API != "openai-completions" {
		t.Fatalf("provider not loaded from Pi shape: %+v", cfg.Providers)
	}
	m, ok := cfg.Models["demo"]
	if !ok || m.Context != 8192 || m.MaxOut != 512 || len(m.Providers) != 1 || m.Providers[0] != "local" {
		t.Fatalf("model not normalized from Pi shape: %+v", cfg.Models)
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Providers map[string]struct {
			Models []PiModel `json:"models"`
		} `json:"providers"`
		Models map[string]Model `json:"models"`
	}
	if err := parseJSONC(data, &wire); err != nil {
		t.Fatal(err)
	}
	if len(wire.Models) != 0 || len(wire.Providers["local"].Models) != 1 || wire.Providers["local"].Models[0].ID != "demo" {
		t.Fatalf("saved config is not Pi provider shape: %+v", wire)
	}
	if saved := wire.Providers["local"].Models[0]; saved.ContextWindow != 8192 || saved.MaxTokens != 512 {
		t.Fatalf("saved limits changed: %+v", saved)
	}
	reloaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Models["demo"].Context != 8192 || reloaded.Models["demo"].MaxOut != 512 {
		t.Fatalf("reloaded limits changed: %+v", reloaded.Models["demo"])
	}
}
