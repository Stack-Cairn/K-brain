package routing

import (
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
)

func TestCatalogPricingFetchSaveAndLoad(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("unexpected catalog request: %s", r.URL.Path)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, `{"data":[
			{"id":"priced","pricing":{"prompt":"0.000002","completion":"0.000003","input_cache_read":"0.0000005","input_cache_write":"0.000004"}},
			{"id":"free-cache","pricing":{"prompt":"0.000002","completion":"0.000003","input_cache_read":"0","input_cache_write":"0"}},
			{"id":"fallback","pricing":{"prompt":"0.000002","completion":"0.000003"}},
			{"id":"free-model","pricing":{"prompt":"0","completion":"0"}},
			{"id":"unpriced"},
			{"id":"incomplete","pricing":{"prompt":"0"}},
			{"id":"invalid","pricing":{"prompt":"0.000002","completion":"0.000003","input_cache_write":"NaN"}}
		]}`)
	}))
	defer srv.Close()
	cfg := &config.Config{Providers: map[string]config.Provider{"provider": {BaseURL: srv.URL + "/v1", APIKey: "test-key"}}}
	RefreshCatalogs(cfg, true)
	loaded := config.LoadCatalogs()
	cat, ok := loaded["provider"]
	if !ok || len(cat.Models) != 7 || cat.BaseURL != srv.URL+"/v1" {
		t.Fatalf("saved catalog = %+v", loaded)
	}
	u := ai.Usage{PromptTokens: 300, CompletionTokens: 30, PromptCacheHitTokens: 180, PromptCacheWriteTokens: 30}
	for id, want := range map[string]float64{"priced": 480e-6, "free-cache": 270e-6, "fallback": 690e-6, "free-model": 0} {
		rates, ok := cat.Pricing(id)
		if !ok {
			t.Fatalf("lost pricing for %s", id)
		}
		cost, ok := ai.CalculateCost(u, rates)
		if !ok || math.Abs(cost.Total-want) > 1e-12 {
			t.Errorf("%s cost = %+v, %v; want %v", id, cost, ok, want)
		}
	}
	for _, id := range []string{"unpriced", "incomplete", "invalid", "missing"} {
		if rates, ok := cat.Pricing(id); ok {
			t.Errorf("%s should be unpriced, got %+v", id, rates)
		}
	}
}
