package tui

import (
	"math"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
)

func TestCacheCostSurvivesModelSwitchCompactionAndResume(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	m := forkModel(t)
	m.agent.Model, m.agent.ModelName, m.agent.Provider = "m", "m", "p"
	m.cfg.Models["worker"] = config.Model{Providers: []string{"p"}}
	m.catalogs = map[string]config.Catalog{
		"p": {Models: []config.ModelInfoLite{
			{ID: "m", Pricing: &ai.TokenRates{Input: 2, Output: 3, CacheRead: 0.5, CacheWrite: 4}},
			{ID: "worker", Pricing: &ai.TokenRates{Input: 5, Output: 7, CacheRead: 1, CacheWrite: 8}},
		}},
		"q": {Models: []config.ModelInfoLite{
			{ID: "worker", Pricing: &ai.TokenRates{Input: 10, Output: 14, CacheRead: 2, CacheWrite: 16}},
		}},
	}
	usage := ai.Usage{PromptTokens: 300, CompletionTokens: 30, PromptCacheHitTokens: 180, PromptCacheWriteTokens: 30}
	m.agent.Messages = append(m.agent.Messages, ai.Message{Role: "user", Content: "priced turn", Authored: true}, ai.Message{Role: "assistant", Content: "first", Model: "m @ p", Usage: &usage})
	m.agent.AddUsage(usage)
	m.switchModel("worker", "p", false)
	if m.agent.Model != "worker" {
		t.Fatal("model switch failed")
	}
	m.agent.Messages = append(m.agent.Messages, ai.Message{Role: "assistant", Content: "second", Model: "worker @ p", Usage: &usage})
	m.agent.AddUsage(usage)
	m.agent.AddSubUsage("worker @ q", usage)
	check := func(stage string) {
		t.Helper()
		if got, ok := m.sessionCost(); !ok || math.Abs(got-3720) > 1e-9 {
			t.Fatalf("%s session cost = %v, %v; want 3720", stage, got, ok)
		}
	}
	check("switched")
	if got, ok := m.turnCost(5); !ok || got != 1560 {
		t.Fatalf("turn cost = %v, %v; want 1560", got, ok)
	}
	if got, ok := m.compactCost(agent.CompactInfo{Model: "worker", Provider: "q", Usage: usage}); !ok || got != 2160 {
		t.Fatalf("compaction cost = %v, %v; want 2160", got, ok)
	}
	m.Update(compactHistoryForTest(m, 3, "summary"))
	check("compacted")
	for range 2 {
		if !m.persist() {
			t.Fatal("session save failed")
		}
	}
	id := m.sessionID
	for range 2 {
		if err := m.resume(id); err != nil {
			t.Fatal(err)
		}
		check("resumed")
		if got, ok := m.turnCost(4); !ok || got != 1560 {
			t.Fatalf("restored turn cost = %v, %v; want 1560", got, ok)
		}
	}
	if _, ok := m.applyRewind(4); !ok {
		t.Fatal("rewind failed")
	}
	check("rewound")
	if got, ok := m.turnCost(4); !ok || got != 1560 {
		t.Fatalf("future turn cost = %v, %v; want 1560", got, ok)
	}
}

func TestTurnCostDoesNotReportPartialOrAmbiguousPrices(t *testing.T) {
	u := ai.Usage{PromptTokens: 100, CompletionTokens: 10, PromptCacheHitTokens: 80, PromptCacheWriteTokens: 10}
	m := rewindModel(t,
		ai.Message{Role: "user", Content: "question", Authored: true},
		ai.Message{Role: "assistant", Content: "priced", Model: "m @ p", Usage: &u},
		ai.Message{Role: "assistant", Content: "unknown", Model: "m @ q", Usage: &u},
	)
	m.catalogs = map[string]config.Catalog{
		"p": {Models: []config.ModelInfoLite{{ID: "m", Pricing: &ai.TokenRates{Input: 2, Output: 3, CacheWrite: 4}}}},
		"q": {Models: []config.ModelInfoLite{{ID: "m"}}},
	}
	if cost, ok := m.turnCost(1); ok || cost != 0 {
		t.Fatalf("reported partial cost: %v, %v", cost, ok)
	}
	if _, ok := m.usageCost("m", "", u); ok {
		t.Fatal("guessed a provider from its pricing")
	}
	m.catalogs["q"].Models[0].Pricing = &ai.TokenRates{}
	if cost, ok := m.turnCost(1); !ok || cost != 90 {
		t.Fatalf("mixed free/paid cost: %v, %v", cost, ok)
	}
	m.catalogs["p"].Models[0].Pricing = &ai.TokenRates{}
	if cost, ok := m.turnCost(1); !ok || cost != 0 {
		t.Fatalf("free turn cost: %v, %v", cost, ok)
	}
	for _, invalid := range []ai.Usage{{PromptCacheWriteTokens: -1}, {PromptTokens: 1, PromptCacheHitTokens: 2}, {CompletionTokens: -1}} {
		if _, ok := m.usageCost("m", "p", invalid); ok {
			t.Fatalf("priced invalid usage: %+v", invalid)
		}
	}
}
