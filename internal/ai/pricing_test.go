package ai

import (
	"encoding/json"
	"math"
	"testing"
)

func TestPricingRates(t *testing.T) {
	for _, tc := range []struct {
		name string
		wire string
		want *TokenRates
	}{
		{"four rates", `{"prompt":"0.000002","completion":"0.000003","input_cache_read":"0.0000005","input_cache_write":"0.000004"}`, &TokenRates{2e-6, 3e-6, 0.5e-6, 4e-6}},
		{"missing cache rates", `{"prompt":"0.000002","completion":"0.000003"}`, &TokenRates{2e-6, 3e-6, 2e-6, 2e-6}},
		{"free cache", `{"prompt":"0.000002","completion":"0.000003","input_cache_read":"0","input_cache_write":"0"}`, &TokenRates{2e-6, 3e-6, 0, 0}},
		{"free model", `{"prompt":"0","completion":"0"}`, &TokenRates{}},
		{"empty", `{}`, nil},
		{"missing input", `{"completion":"0.000003"}`, nil},
		{"missing output", `{"prompt":"0.000002"}`, nil},
		{"whitespace", `{"prompt":" 2e-6 ","completion":" 3e-6 ","input_cache_read":" 0 "}`, &TokenRates{2e-6, 3e-6, 0, 2e-6}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var pricing Pricing
			if err := json.Unmarshal([]byte(tc.wire), &pricing); err != nil {
				t.Fatal(err)
			}
			got := pricing.Rates()
			if (got == nil) != (tc.want == nil) || got != nil && *got != *tc.want {
				t.Fatalf("rates = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestPricingRejectsInvalidRates(t *testing.T) {
	for _, field := range []string{"prompt", "completion", "input_cache_read", "input_cache_write"} {
		for _, value := range []string{"bad", "NaN", "Inf", "-Inf", "-1", "1e999", " "} {
			t.Run(field+"/"+value, func(t *testing.T) {
				wire, err := json.Marshal(map[string]string{"prompt": "2", "completion": "3", field: value})
				if err != nil {
					t.Fatal(err)
				}
				var pricing Pricing
				if err := json.Unmarshal(wire, &pricing); err != nil {
					t.Fatal(err)
				}
				if rates := pricing.Rates(); rates != nil {
					t.Fatalf("invalid pricing accepted: %+v", rates)
				}
			})
		}
	}
}

func TestCalculateCostCacheBreakdown(t *testing.T) {
	u := Usage{PromptTokens: 300, CompletionTokens: 30, PromptCacheHitTokens: 180, PromptCacheWriteTokens: 30}
	rates := TokenRates{Input: 2, Output: 3, CacheRead: 0.5, CacheWrite: 4}
	cost, ok := CalculateCost(u, rates)
	if !ok || cost != (Cost{Input: 180, Output: 90, CacheRead: 90, CacheWrite: 120, Total: 480}) {
		t.Fatalf("cost = %+v, %v", cost, ok)
	}
	rates.CacheRead, rates.CacheWrite = 0, 0
	if cost, ok = CalculateCost(u, rates); !ok || cost.Total != 270 || cost.CacheRead != 0 || cost.CacheWrite != 0 {
		t.Fatalf("free cache cost = %+v, %v", cost, ok)
	}
	if cost, ok = CalculateCost(u, TokenRates{}); !ok || cost != (Cost{}) {
		t.Fatalf("free model cost = %+v, %v", cost, ok)
	}
}

func TestCalculateCostRejectsInvalidUsage(t *testing.T) {
	negativeDetails := Usage{PromptTokens: 100}
	negativeDetails.PromptTokensDetails = &struct {
		CachedTokens int `json:"cached_tokens"`
	}{CachedTokens: -1}
	for _, u := range []Usage{
		{PromptTokens: -1},
		{CompletionTokens: -1},
		{PromptTokens: 100, PromptCacheHitTokens: -1},
		{PromptTokens: 100, PromptCacheWriteTokens: -1},
		negativeDetails,
		{PromptTokens: 100, PromptCacheHitTokens: 101},
		{PromptTokens: 100, PromptCacheWriteTokens: 101},
		{PromptTokens: 100, PromptCacheHitTokens: 80, PromptCacheWriteTokens: 30},
		{PromptTokens: math.MaxInt, PromptCacheHitTokens: math.MaxInt, PromptCacheWriteTokens: math.MaxInt},
	} {
		if cost, ok := CalculateCost(u, TokenRates{1, 1, 1, 1}); ok || cost != (Cost{}) {
			t.Fatalf("accepted usage %+v: %+v, %v", u, cost, ok)
		}
	}
	if cost, ok := CalculateCost(Usage{PromptTokens: 100, PromptCacheHitTokens: 80, PromptCacheWriteTokens: 20}, TokenRates{1, 1, 1, 2}); !ok || cost.Input != 0 || cost.Total != 120 {
		t.Fatalf("fully cached prompt: %+v, %v", cost, ok)
	}
}

func TestCalculateCostRejectsInvalidAndOverflowingPrices(t *testing.T) {
	for _, rate := range []float64{-1, math.NaN(), math.Inf(1), math.Inf(-1), math.MaxFloat64} {
		for i := range 4 {
			rates := TokenRates{1, 1, 1, 1}
			fields := []*float64{&rates.Input, &rates.Output, &rates.CacheRead, &rates.CacheWrite}
			*fields[i] = rate
			if cost, ok := CalculateCost(Usage{PromptTokens: 6, CompletionTokens: 2, PromptCacheHitTokens: 2, PromptCacheWriteTokens: 2}, rates); ok || cost != (Cost{}) {
				t.Fatalf("accepted rates %+v: %+v, %v", rates, cost, ok)
			}
		}
	}
}
