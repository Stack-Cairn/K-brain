package config

import (
	"os"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func TestCatalogPricing(t *testing.T) {
	cat := Catalog{Models: []ModelInfoLite{
		{ID: "priced", Pricing: &ai.TokenRates{Input: 1e-6, Output: 5e-6, CacheRead: 1e-7, CacheWrite: 2e-6}},
		{ID: "free", Pricing: &ai.TokenRates{}},
		{ID: "unpriced"},
	}}
	rates, ok := cat.Pricing("priced")
	if !ok || rates != (ai.TokenRates{Input: 1e-6, Output: 5e-6, CacheRead: 1e-7, CacheWrite: 2e-6}) {
		t.Fatalf("priced model: %+v ok=%v", rates, ok)
	}
	if _, ok := cat.Pricing("free"); !ok {
		t.Fatal("explicit free model should have pricing")
	}
	if _, ok := cat.Pricing("unpriced"); ok {
		t.Fatal("model with no prices should report ok=false")
	}
	if _, ok := cat.Pricing("missing"); ok {
		t.Fatal("unknown model should report ok=false")
	}
}

func TestCatalogPricingRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	cats := map[string]Catalog{
		"p": {Models: []ModelInfoLite{{ID: "m", Pricing: &ai.TokenRates{Input: 1e-6, Output: 5e-6, CacheRead: 1e-7, CacheWrite: 2e-6}}}},
	}
	if err := SaveCatalogs(cats); err != nil {
		t.Fatal(err)
	}
	got := LoadCatalogs()
	rates, ok := got["p"].Pricing("m")
	if !ok || rates != *cats["p"].Models[0].Pricing {
		t.Fatalf("round-trip: %+v ok=%v", rates, ok)
	}
}

func TestCatalogStale(t *testing.T) {
	if (Catalog{FetchedAt: time.Now()}).Stale() {
		t.Fatal("just-fetched catalog must be fresh")
	}
	if !(Catalog{FetchedAt: time.Now().Add(-25 * time.Hour)}).Stale() {
		t.Fatal("day-old catalog must be stale")
	}
	if !(Catalog{}).Stale() {
		t.Fatal("zero-value catalog must be stale")
	}
}

func TestCatalogSupportsVision(t *testing.T) {
	cat := Catalog{Models: []ModelInfoLite{
		{ID: "vision", InputModalities: []string{"text", "image"}},
		{ID: "textonly", InputModalities: []string{"text"}},
		{ID: "unadvertised"},
	}}
	cases := []struct {
		id            string
		vision, found bool
	}{
		{"vision", true, true},
		{"textonly", false, true},
		{"unadvertised", false, false},
		{"missing", false, false},
	}
	for _, c := range cases {
		if v, f := cat.SupportsVision(c.id); v != c.vision || f != c.found {
			t.Errorf("SupportsVision(%q) = %v,%v; want %v,%v", c.id, v, f, c.vision, c.found)
		}
	}
}
