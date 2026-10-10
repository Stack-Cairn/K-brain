package config

import (
	"slices"
	"testing"
)

func TestParseConfigKeepsPerProviderSharedModelMetadata(t *testing.T) {
	input := `{"providers":{"a":{"api":"openai-completions","baseUrl":"https://a.invalid/v1","models":[{"id":"shared","contextWindow":100}]},"b":{"api":"openai-completions","baseUrl":"https://b.invalid/v1","models":[{"id":"shared","contextWindow":200,"maxTokens":50,"input":["text","image"]}]}}}`
	var cfg Config
	if err := parseConfigJSONC([]byte(input), &cfg); err != nil {
		t.Fatal(err)
	}
	m := cfg.Models["shared"]
	if !slices.Equal(m.Providers, []string{"a", "b"}) {
		t.Fatalf("providers = %v", m.Providers)
	}
	if a := m.ForProvider("a"); a.Context != 100 || a.MaxOut != 0 || a.Vision {
		t.Fatalf("a = %+v", a)
	}
	if b := m.ForProvider("b"); b.Context != 200 || b.MaxOut != 50 || !b.Vision || b.ID != "shared" || !slices.Equal(b.Providers, m.Providers) {
		t.Fatalf("b = %+v", b)
	}
}

func TestParseConfigIdenticalSharedModelHasNoOverrides(t *testing.T) {
	input := `{"providers":{"a":{"api":"openai-completions","baseUrl":"https://a.invalid/v1","models":[{"id":"shared","contextWindow":100}]},"b":{"api":"openai-completions","baseUrl":"https://b.invalid/v1","models":[{"id":"shared","contextWindow":100}]}}}`
	var cfg Config
	if err := parseConfigJSONC([]byte(input), &cfg); err != nil {
		t.Fatal(err)
	}
	if m := cfg.Models["shared"]; m.ProviderMetadata != nil || m.Context != 100 {
		t.Fatalf("model = %+v", m)
	}
}

func TestMarshalConfigRoundTripsPerProviderSharedModelMetadata(t *testing.T) {
	m := Model{ID: "shared", Providers: []string{"a", "b"}, Context: 100}
	m.SetProviderMetadata("b", Model{Context: 200, MaxOut: 50, InputModalities: []string{"text", "image"}, Vision: true})
	cfg := &Config{Providers: map[string]Provider{
		"a": {API: "openai-completions", BaseURL: "https://a.invalid/v1"},
		"b": {API: "openai-completions", BaseURL: "https://b.invalid/v1"},
	}, Models: map[string]Model{"shared": m}}
	data, err := marshalConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var back Config
	if err := parseConfigJSONC(data, &back); err != nil {
		t.Fatal(err)
	}
	got := back.Models["shared"]
	if a := got.ForProvider("a"); a.Context != 100 || a.MaxOut != 0 || a.Vision {
		t.Fatalf("a = %+v", a)
	}
	if b := got.ForProvider("b"); b.Context != 200 || b.MaxOut != 50 || !b.Vision || !slices.Equal(b.InputModalities, []string{"text", "image"}) {
		t.Fatalf("b = %+v", b)
	}
	if snap := back.Snapshot().Models["shared"].ForProvider("b"); snap.Context != 200 {
		t.Fatalf("snapshot lost override: %+v", snap)
	}
}

func TestResolveUsesProviderMetadata(t *testing.T) {
	m := Model{ID: "shared", Providers: []string{"a", "b"}, Context: 100}
	m.SetProviderMetadata("b", Model{Context: 200})
	cfg := &Config{Providers: map[string]Provider{"a": {}, "b": {}}, Models: map[string]Model{"shared": m}}
	for provider, want := range map[string]int{"a": 100, "b": 200, "": 100} {
		_, got, _, err := cfg.Resolve("shared", provider)
		if err != nil || got.ContextWindow() != want {
			t.Fatalf("Resolve(%q) = %d, %v; want %d", provider, got.ContextWindow(), err, want)
		}
	}
}

func TestDropProviderPromotesRemainingOverride(t *testing.T) {
	m := Model{ID: "shared", Providers: []string{"a", "b"}, Context: 100}
	m.SetProviderMetadata("b", Model{Context: 200})
	m.DropProvider("a")
	if m.Context != 200 || m.ProviderMetadata != nil || !slices.Equal(m.Providers, []string{"b"}) {
		t.Fatalf("model = %+v", m)
	}
}

func TestRemoveProviderKeepsOtherProvidersMetadata(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	m := Model{ID: "shared", Providers: []string{"a", "b", "c"}, Context: 100}
	m.SetProviderMetadata("c", Model{Context: 300})
	cfg := &Config{Providers: map[string]Provider{"a": {}, "b": {}, "c": {}}, Models: map[string]Model{"shared": m}}
	cfg.RemoveProvider("a")
	got := cfg.Models["shared"]
	if got.ForProvider("b").Context != 100 || got.ForProvider("c").Context != 300 || !slices.Equal(got.Providers, []string{"b", "c"}) {
		t.Fatalf("model = %+v", got)
	}
}
