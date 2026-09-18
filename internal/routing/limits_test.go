package routing

import (
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

func TestTokenLimits(t *testing.T) {
	cat := config.Catalog{Models: []config.ModelInfoLite{{ID: "model1", ContextLength: 128000, MaxCompletionTokens: 16384}}}
	for _, tc := range []struct {
		name            string
		model           config.Model
		catalog         config.Catalog
		context, output int
	}{
		{"configured-smaller", config.Model{Context: 32000, MaxOut: 4096}, cat, 32000, 4096},
		{"configured-larger", config.Model{Context: 256000, MaxOut: 32768}, cat, 256000, 32768},
		{"catalog", config.Model{}, cat, 128000, 16384},
		{"configured-context", config.Model{Context: 64000}, cat, 64000, 16384},
		{"configured-output", config.Model{MaxOut: 2048}, cat, 128000, 2048},
		{"no-catalog", config.Model{Context: 64000}, config.Catalog{}, 64000, 64000},
		{"unknown", config.Model{}, config.Catalog{}, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, out := TokenLimits(tc.model, tc.catalog, "model1")
			if ctx != tc.context || out != tc.output {
				t.Fatalf("limits = (%d, %d), want (%d, %d)", ctx, out, tc.context, tc.output)
			}
		})
	}
}

func TestSubModelConfiguredLimitsOverrideCatalog(t *testing.T) {
	cfg := taskCfg(t, "http://x")
	cfg.DefaultProvider = "other"
	cfg.Providers["other"] = config.Provider{BaseURL: "http://other", APIKey: "k"}
	cfg.Models["m"] = config.Model{Providers: []string{"p"}, ID: "model1", Context: 64000, MaxOut: 4096}
	if err := config.SaveCatalogs(map[string]config.Catalog{
		"other": {Models: []config.ModelInfoLite{{ID: "model1", ContextLength: 128000, MaxCompletionTokens: 8192}}},
	}); err != nil {
		t.Fatal(err)
	}
	for _, configured := range []bool{true, false} {
		wantCtx, wantOut := 64000, 4096
		if !configured {
			mdl := cfg.Models["m"]
			mdl.Context, mdl.MaxOut = 0, 0
			cfg.Models["m"] = mdl
			wantCtx, wantOut = 128000, 8192
		}
		sub, err := SubModelFor(cfg, "", "")
		if err != nil {
			t.Fatal(err)
		}
		if sub.Model != "model1" || sub.ContextLimit != wantCtx || sub.MaxTokens != wantOut {
			t.Fatalf("configured=%v: sub = %+v", configured, sub)
		}
	}
}
