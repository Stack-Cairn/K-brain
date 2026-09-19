package routing

import (
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func TestCatalogLites(t *testing.T) {
	lites := CatalogModels([]ai.ModelInfo{
		{
			ID: "openai/gpt-5", ContextLength: 400000, MaxCompletionTokens: 128000,
			ReasoningEfforts: []string{"low", "high"}, InputModalities: []string{"text", "image"},
			Pricing: &ai.Pricing{Prompt: "0.00000125", Completion: "0.00001", InputCacheRead: "0.000000125"},
		},
		{ID: "meta/llama-4", ContextLength: 131072},
	})
	if len(lites) != 2 {
		t.Fatalf("want 2 lites, got %d", len(lites))
	}
	a := lites[0]
	if a.ContextLength != 400000 || a.MaxCompletionTokens != 128000 {
		t.Errorf("caps not carried: %+v", a)
	}
	if len(a.ReasoningEfforts) != 2 || len(a.InputModalities) != 2 {
		t.Errorf("efforts/modalities not carried: %+v", a)
	}
	if a.Pricing == nil || a.Pricing.Input == 0 || a.Pricing.Output == 0 || a.Pricing.CacheRead == 0 || a.Pricing.CacheWrite != a.Pricing.Input {
		t.Errorf("pricing not parsed: %+v", a)
	}
	if b := lites[1]; b.Pricing != nil || len(b.InputModalities) != 0 {
		t.Errorf("pricing-less model should stay zero-rated: %+v", b)
	}
}
