package config

import (
	"encoding/json"
	"reflect"
	"testing"
)

// olderBuildAccepts mirrors the load-time rule of K-brain builds before per-provider model
// metadata: every provider listing a model ID must declare byte-identical metadata for it,
// and unknown fields are rejected (only the free-form provider metadata map is open).
func olderBuildAccepts(t *testing.T, data []byte) {
	t.Helper()
	var wire struct {
		Providers map[string]Provider `json:"providers"`
	}
	stripped, err := stripJSONC(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(stripped, &wire); err != nil {
		t.Fatal(err)
	}
	seen := map[string]PiModel{}
	for name, provider := range wire.Providers {
		for _, model := range provider.Models {
			if previous, ok := seen[model.ID]; ok && !reflect.DeepEqual(previous, model) {
				t.Fatalf("an older K-brain would refuse this config: model %q differs in provider %q", model.ID, name)
			}
			seen[model.ID] = model
		}
	}
}

func TestPerProviderMetadataIsWrittenSoOlderBuildsStillLoad(t *testing.T) {
	m := Model{ID: "shared", Providers: []string{"a", "b"}, Context: 100}
	m.SetProviderMetadata("b", Model{Context: 200, MaxOut: 50, InputModalities: []string{"text", "image"}, Vision: true})
	cfg := &Config{Providers: map[string]Provider{
		"a": {API: "openai-completions", BaseURL: "https://a.invalid/v1"},
		"b": {API: "openai-completions", BaseURL: "https://b.invalid/v1", Metadata: map[string]any{"vendor": "relay"}},
	}, Models: map[string]Model{"shared": m}, DefaultModel: "shared"}
	data, err := marshalConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	olderBuildAccepts(t, data)

	var back Config
	if err := parseConfigJSONC(data, &back); err != nil {
		t.Fatal(err)
	}
	if b := back.Models["shared"].ForProvider("b"); b.Context != 200 || b.MaxOut != 50 || !b.Vision {
		t.Fatalf("override lost: %+v", b)
	}
	if a := back.Models["shared"].ForProvider("a"); a.Context != 100 || a.Vision {
		t.Fatalf("shared metadata changed: %+v", a)
	}
	// The overrides entry is a storage detail: unrelated provider metadata survives, the entry does not.
	if got := back.Providers["b"].Metadata; !reflect.DeepEqual(got, map[string]any{"vendor": "relay"}) {
		t.Fatalf("provider metadata = %#v", got)
	}
	if got := back.Providers["a"].Metadata; got != nil {
		t.Fatalf("provider a metadata = %#v", got)
	}

	// Saving again is stable.
	again, err := marshalConfig(&back)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(data) {
		t.Fatalf("second save differs:\n%s\n---\n%s", data, again)
	}
}

func TestConfigWithDivergentProviderBlocksStillLoads(t *testing.T) {
	// Files written by an earlier revision of this change list each provider's own variant.
	data := []byte(`{"providers":{
		"a":{"api":"openai-completions","baseUrl":"https://a.invalid/v1","models":[{"id":"shared","contextWindow":100}]},
		"b":{"api":"openai-completions","baseUrl":"https://b.invalid/v1","models":[{"id":"shared","contextWindow":200}]}}}`)
	var cfg Config
	if err := parseConfigJSONC(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if got := cfg.Models["shared"].ForProvider("b").Context; got != 200 {
		t.Fatalf("b context = %d", got)
	}
	out, err := marshalConfig(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	olderBuildAccepts(t, out)
}

func TestInvalidModelOverridesAreRejected(t *testing.T) {
	data := []byte(`{"providers":{"a":{"api":"openai-completions","baseUrl":"https://a.invalid/v1",
		"metadata":{"kbrainModelMetadataOverrides":{"shared":{"id":"shared","contextWindow":-1}}},
		"models":[{"id":"shared","contextWindow":100}]}}}`)
	var cfg Config
	if err := parseConfigJSONC(data, &cfg); err == nil {
		t.Fatal("negative override context accepted")
	}
}
