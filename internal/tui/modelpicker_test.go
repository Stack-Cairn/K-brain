package tui

import (
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

func TestBuildModelItems(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.Provider{
			"a": {BaseURL: "https://a"},
			"b": {BaseURL: "https://b"},
		},
		Models: map[string]config.Model{
			"zeta":  {Providers: []string{"b", "a"}},
			"alpha": {Providers: []string{"a", "ghost"}},
		},
	}
	items := buildModelItems(cfg)
	if len(items) != 4 {
		t.Fatalf("items: %+v", items)
	}

	if items[0].model != "alpha" || items[2].model != "zeta" {
		t.Fatalf("model order: %+v", items)
	}

	if items[2].provider != "b" || items[3].provider != "a" {
		t.Fatalf("provider order: %+v", items)
	}
	if items[0].url != "https://a" {
		t.Fatalf("url: %+v", items[0])
	}
	if items[1].provider != "ghost" || items[1].url != "" {
		t.Fatalf("unknown provider should keep empty url: %+v", items[1])
	}
	if got := buildModelItems(&config.Config{}); len(got) != 0 {
		t.Fatalf("empty config: %+v", got)
	}
}

func TestModelPickerFilter(t *testing.T) {
	p := &modelPicker{items: []modelItem{
		{model: "alpha", provider: "a"},
		{model: "beta", provider: "a"},
		{model: "beta", provider: "b"},
		{model: "gamma", provider: "c"},
	}}

	if got := len(p.view()); got != 4 {
		t.Fatalf("unfiltered view: %d", got)
	}

	p.filter.query = "BET"
	p.applyQuery()
	if got := p.view(); len(got) != 2 || got[0].model != "beta" {
		t.Fatalf("filter by model: %+v", got)
	}

	p.filter.query = "c"
	p.applyQuery()
	if got := p.view(); len(got) != 1 || got[0].provider != "c" {
		t.Fatalf("filter by provider: %+v", got)
	}

	p.filter.query = "zzz"
	p.applyQuery()
	if got := p.view(); len(got) != 0 {
		t.Fatalf("no-match view: %+v", got)
	}

	p.filter.query = ""
	p.applyQuery()
	if got := len(p.view()); got != 4 {
		t.Fatalf("cleared query view: %d", got)
	}
}

func TestModelPickerFuzzyFilter(t *testing.T) {
	p := &modelPicker{items: []modelItem{
		{model: "claude-opus-4", provider: "a"},
		{model: "gpt-5", provider: "openai"},
	}}

	p.filter.query = "claudopus"
	p.applyQuery()
	if got := p.view(); len(got) != 1 || got[0].model != "claude-opus-4" {
		t.Fatalf("subsequence filter: %+v", got)
	}

	p.filter.query = "openai"
	p.applyQuery()
	if got := p.view(); len(got) != 1 || got[0].model != "gpt-5" {
		t.Fatalf("provider subsequence filter: %+v", got)
	}
}

func TestResolveModelFuzzy(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.Provider{"a": {BaseURL: "https://a"}},
		Models: map[string]config.Model{
			"claude-sonnet-4": {Providers: []string{"a"}},
			"claude-opus-4":   {Providers: []string{"a"}},
		},
	}

	if got, ok, _ := resolveModelFuzzy(cfg, "claude-opus-4"); !ok || got != "claude-opus-4" {
		t.Fatalf("exact passthrough: %q %v", got, ok)
	}

	if got, ok, _ := resolveModelFuzzy(cfg, "sonnet"); !ok || got != "claude-sonnet-4" {
		t.Fatalf("substring resolve: %q %v", got, ok)
	}

	got, ok, alts := resolveModelFuzzy(cfg, "claude")
	if ok || len(alts) != 2 {
		t.Fatalf("ambiguous resolve: %q %v %v", got, ok, alts)
	}

	if _, ok, _ := resolveModelFuzzy(cfg, "zzz"); ok {
		t.Fatal("no-match should not resolve")
	}
}
