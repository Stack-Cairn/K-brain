package backend

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func TestUsageStatsHTTP(t *testing.T) {
	store, err := session.OpenProjectDir(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now()
	for i, cwd := range []string{"/work/one", "/work/two"} {
		id, err := store.Create(cwd, "fixture-model", "fixture")
		if err != nil {
			t.Fatal(err)
		}
		sent := now.Add(-time.Duration(i) * time.Minute)
		msgs := []ai.Message{
			{Role: "user", Content: "hello", SentAt: &sent},
			{ID: "msg-" + cwd, Role: "assistant", Content: "hi", SentAt: &sent, Model: "fixture-model @ fixture",
				Usage: &ai.Usage{PromptTokens: 100 * (i + 1), CompletionTokens: 10, PromptCacheHitTokens: 40}},
		}
		if err := store.Save(id, 0, msgs, "fixture-model", "fixture"); err != nil {
			t.Fatal(err)
		}
	}
	_, server := newTestServer(t, store, t.TempDir(), &scriptedClient{response: "ok"})

	get := func(query string) (*http.Response, map[string]any) {
		t.Helper()
		resp, err := http.Get(server.URL + "/v1/usage/stats" + query)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var body map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		return resp, body
	}

	resp, body := get("?days=5000&tz=UTC")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %v", resp.StatusCode, body)
	}
	if body["timezone"] != "UTC" || body["days"] != float64(usageStatsMaxDays) {
		t.Fatalf("timezone/days = %v/%v", body["timezone"], body["days"])
	}
	if _, err := time.Parse(time.RFC3339, body["generatedAt"].(string)); err != nil {
		t.Fatalf("generatedAt = %v", body["generatedAt"])
	}
	totals := body["totals"].(map[string]any)
	want := map[string]any{"input": 300.0, "output": 20.0, "cacheRead": 80.0, "cacheWrite": 0.0, "total": 320.0, "turns": 2.0, "sessions": 2.0, "costUSD": nil}
	for k, v := range want {
		if got, ok := totals[k]; !ok || got != v {
			t.Fatalf("totals[%s] = %v (present %v), want %v", k, got, ok, v)
		}
	}
	summary := body["summary"].(map[string]any)
	if summary["activeDays"].(float64) < 1 || summary["currentStreak"].(float64) < 1 || summary["topModel"] != "fixture-model" {
		t.Fatalf("summary = %v", summary)
	}
	if longest := summary["longestSession"].(map[string]any); longest["total"] != 210.0 || longest["title"] != "hello" || longest["id"] == "" {
		t.Fatalf("longestSession = %v", longest)
	}
	daily := body["daily"].([]any)
	if len(daily) == 0 {
		t.Fatal("no daily rows")
	}
	models := body["models"].([]any)
	if len(models) != 1 {
		t.Fatalf("models = %v", models)
	}
	model := models[0].(map[string]any)
	if model["model"] != "fixture-model" || model["provider"] != "fixture" || model["total"] != 320.0 || model["turns"] != 2.0 {
		t.Fatalf("model = %v", model)
	}
	if v, ok := model["costUSD"]; !ok || v != nil {
		t.Fatalf("unpriced model costUSD = %v (present %v)", v, ok)
	}

	// Defaults: 371 days in the process's zone; an empty range still lists arrays, not null.
	if resp, body := get(""); resp.StatusCode != http.StatusOK || body["days"] != float64(usageStatsDefaultDays) || body["timezone"] == "" {
		t.Fatalf("defaults: %d %v", resp.StatusCode, body)
	}
	if resp, body := get("?days=0&tz=Asia/Shanghai"); resp.StatusCode != http.StatusOK || body["days"] != 1.0 || body["timezone"] != "Asia/Shanghai" {
		t.Fatalf("clamped: %d %v", resp.StatusCode, body)
	}
	for _, bad := range []string{"?tz=Mars/Olympus", "?tz=Local", "?days=week"} {
		if resp, body := get(bad); resp.StatusCode != http.StatusBadRequest || body["error"] == nil {
			t.Fatalf("%s: %d %v", bad, resp.StatusCode, body)
		}
	}
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/usage/stats", nil)
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST: %v %v", resp, err)
	}

	hresp, err := http.Get(server.URL + "/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	defer hresp.Body.Close()
	var health map[string]any
	if err := json.NewDecoder(hresp.Body).Decode(&health); err != nil {
		t.Fatal(err)
	}
	if caps := health["capabilities"].(map[string]any); caps["usage_stats"] != 1.0 {
		t.Fatalf("capabilities.usage_stats = %v", caps["usage_stats"])
	}
}

func TestUsageStatsCost(t *testing.T) {
	stats := session.UsageStats{Models: []session.UsageModel{
		{Model: "priced", Provider: "p", UsageTokens: session.UsageTokens{Input: 1000, Output: 100, CacheRead: 400}},
		{Model: "unknown", Provider: "p", UsageTokens: session.UsageTokens{Input: 10, Output: 1}},
		{Model: "priced", Provider: "sub", UsageTokens: session.UsageTokens{Input: 10, Output: 1}},
	}}
	rates := &ai.TokenRates{Input: 0.000003, Output: 0.000015, CacheRead: 0.0000003, CacheWrite: 0.00000375}
	cats := map[string]config.Catalog{
		"p":   {Models: []config.ModelInfoLite{{ID: "priced", Pricing: rates}}},
		"sub": {Models: []config.ModelInfoLite{{ID: "priced", Pricing: rates}}},
	}
	cfg := &config.Config{Providers: map[string]config.Provider{"p": {API: "openai-completions"}, "sub": {API: "openai-codex-responses"}}}
	out := buildUsageStatsResponse(stats, cfg, cats, time.Now(), "UTC", 7)
	// 600 uncached input, 400 cached input and 100 output tokens.
	want := 600*0.000003 + 400*0.0000003 + 100*0.000015
	if c := out.Models[0].CostUSD; c == nil || *c != roundUSD(want) {
		t.Fatalf("priced cost = %v, want %v", c, want)
	}
	if out.Models[1].CostUSD != nil || out.Models[2].CostUSD != nil {
		t.Fatalf("unpriced/subscription costs = %v %v", out.Models[1].CostUSD, out.Models[2].CostUSD)
	}
	if c := out.Totals.CostUSD; c == nil || *c != roundUSD(want) {
		t.Fatalf("total cost = %v", c)
	}
	none := buildUsageStatsResponse(session.UsageStats{Models: stats.Models[1:]}, cfg, cats, time.Now(), "UTC", 7)
	if none.Totals.CostUSD != nil {
		t.Fatalf("no priced usage: total cost = %v", *none.Totals.CostUSD)
	}
	data, err := json.Marshal(none)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	_ = json.Unmarshal(data, &raw)
	if v, ok := raw["totals"].(map[string]any)["costUSD"]; !ok || v != nil {
		t.Fatalf("costUSD must be null, got %v (present %v)", v, ok)
	}
	if raw["summary"].(map[string]any)["longestSession"] != nil || raw["daily"] == nil {
		t.Fatalf("empty stats = %s", data)
	}
}
