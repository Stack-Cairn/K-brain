package tui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
)

func compactCmdModel() *model {

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"content":"sim"}}]}`))
	}))
	m := &model{
		input:   newInput(),
		mouseOn: true,
		agent:   agent.New(ai.New(srv.URL, "k"), "kimi-k3-fast", 100, "sys"),
		cfg: &config.Config{
			DefaultModel: "kimi-k3-fast",
			Providers:    map[string]config.Provider{"inference": {BaseURL: "https://x", APIKey: "k"}},
			Models: map[string]config.Model{
				"kimi-k3-fast": {Providers: []string{"inference"}},
				"glm-5.2-fast": {Providers: []string{"inference"}},

				config.DefaultCompactModel: {Providers: []string{"inference"}},
			},
		},
		modelName: "kimi-k3-fast",
		provName:  "inference",
		catalogs: map[string]config.Catalog{
			"inference": {Models: []config.ModelInfoLite{
				{ID: "kimi-k3-fast", ContextLength: 131072},
			}},
		},
	}
	m.width = 80
	m.input.SetWidth(78)
	return m
}

func TestCompactCommandNeverTouchesRealHome(t *testing.T) {
	m := compactCmdModel()
	m.compactCommand([]string{"glm-5.2-fast"})
	dir := os.Getenv("K_BRAIN_HOME")
	if dir == "" || dir == filepath.Join(os.Getenv("HOME"), ".k-brain") {
		t.Fatalf("tests must run with an isolated K_BRAIN_HOME, got %q", dir)
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json")); err != nil {
		t.Fatalf("expected the save to land under K_BRAIN_HOME: %v", err)
	}
}

func TestCompactCommandSelectsModel(t *testing.T) {
	m := compactCmdModel()
	m.compactCommand([]string{"glm-5.2-fast"})
	if m.compactModel != "glm-5.2-fast" || m.compactProv != "" {
		t.Fatalf("compact model state: %q @ %q", m.compactModel, m.compactProv)
	}
	if m.agent.CompactModel != "glm-5.2-fast" || m.agent.CompactClient == nil {
		t.Fatalf("agent should summarize with glm-5.2-fast on its own client")
	}
	if m.cfg.CompactModel != "glm-5.2-fast" {
		t.Fatalf("config should persist the pick, got %q", m.cfg.CompactModel)
	}
	m.compactCommand([]string{"off"})
	if m.compactModel != "" || m.agent.CompactModel != config.DefaultCompactModel || m.agent.CompactClient == nil {
		t.Fatalf("off should restore the default compaction model: %q", m.compactModel)
	}
	if !strings.Contains(m.blocks[len(m.blocks)-1].text, "default ("+config.DefaultCompactModel+")") {
		t.Fatalf("off should note the default, got %v", m.blocks[len(m.blocks)-1].text)
	}
}

func TestCompactModelEmptyResolvesDefault(t *testing.T) {
	m := compactCmdModel()
	m.applyCompactModel()
	if m.agent.CompactModel != config.DefaultCompactModel || m.agent.CompactClient == nil || m.agent.CompactProvider != "inference" {
		t.Fatalf("empty compactModel should resolve the default, got %q", m.agent.CompactModel)
	}
}

func TestCompactModelDefaultDoesNotUseSessionDefaultProvider(t *testing.T) {
	m := compactCmdModel()
	m.cfg.DefaultProvider = "other"
	m.cfg.Providers["other"] = config.Provider{
		BaseURL: "https://other.example/v1",
		API:     "openai-completions",
		APIKey:  "key",
	}

	m.applyCompactModel()

	if m.agent.CompactModel != config.DefaultCompactModel || m.agent.CompactClient == nil {
		t.Fatalf("default summary should use inference route, got model %q client %T", m.agent.CompactModel, m.agent.CompactClient)
	}
}

func TestCompactModelDefaultFallsBack(t *testing.T) {
	m := compactCmdModel()
	delete(m.cfg.Models, config.DefaultCompactModel)
	blocks := len(m.blocks)
	m.applyCompactModel()
	if m.agent.CompactClient != nil || m.agent.CompactModel != "" {
		t.Fatal("unresolvable default should fall back to the current model")
	}
	if len(m.blocks) != blocks {
		t.Fatal("a missing default should not nag — only picked models earn an error note")
	}
}

func TestCompactCommandRejectsUnknownModel(t *testing.T) {
	m := compactCmdModel()
	m.compactCommand([]string{"nope"})
	if m.compactModel != "" || m.agent.CompactModel != "" {
		t.Fatal("unknown model must not become the compaction model")
	}
	if !strings.Contains(m.blocks[len(m.blocks)-1].text, "unknown model") {
		t.Fatalf("expected an error note, got %v", m.blocks)
	}
}

func TestCompactCommandSelectsCatalogModel(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	if err := config.SaveCatalogs(map[string]config.Catalog{
		"inference": {Models: []config.ModelInfoLite{{ID: "deepseek-v4-pro", ContextLength: 1048576}}},
	}); err != nil {
		t.Fatal(err)
	}
	m := compactCmdModel()
	m.compactCommand([]string{"deepseek-v4-pro"})
	if m.compactModel != "deepseek-v4-pro" || m.agent.CompactModel != "deepseek-v4-pro" || m.agent.CompactClient == nil {
		t.Fatalf("catalog model should be picked and applied: %q / %q", m.compactModel, m.agent.CompactModel)
	}
	if m.cfg.CompactModel != "deepseek-v4-pro" {
		t.Fatalf("config should persist the catalog pick, got %q", m.cfg.CompactModel)
	}
	if !strings.Contains(m.blocks[len(m.blocks)-1].text, "deepseek-v4-pro@inference") {
		t.Fatalf("the note should name the resolved provider, got %v", m.blocks[len(m.blocks)-1].text)
	}
}

func TestCompactCommandResolvesFuzzy(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	if err := config.SaveCatalogs(map[string]config.Catalog{
		"inference": {Models: []config.ModelInfoLite{{ID: "deepseek-v4-pro", ContextLength: 1048576}}},
	}); err != nil {
		t.Fatal(err)
	}
	m := compactCmdModel()
	m.compactCommand([]string{"deepseek-v4-pr"})
	if m.compactModel != "deepseek-v4-pro" {
		t.Fatalf("a fuzzy hit should pick the catalog model, got %q", m.compactModel)
	}
}

func TestContextLimitFromCatalog(t *testing.T) {
	m := compactCmdModel()
	if got := m.contextLimitFor("inference", "kimi-k3-fast"); got != 131072 {
		t.Fatalf("contextLimitFor: %d", got)
	}
	if got := m.contextLimitFor("inference", "unknown"); got != 0 {
		t.Fatalf("unknown model: %d", got)
	}

	cats := map[string]config.Catalog{
		"inference": {Models: []config.ModelInfoLite{{ID: "kimi-k3-fast", ContextLength: 262144}}},
	}
	m.updateCatalogs(cats)
	if m.agent.ContextLimit != 262144 {
		t.Fatalf("agent limit should follow the catalog, got %d", m.agent.ContextLimit)
	}
}

func TestCatalogRefreshKeepsConfiguredFallback(t *testing.T) {
	m := compactCmdModel()
	m.agent.ContextLimit = 131072
	m.updateCatalogs(map[string]config.Catalog{})
	if m.agent.ContextLimit != 131072 {
		t.Fatalf("unknown catalog limit should preserve the configured fallback, got %d", m.agent.ContextLimit)
	}
}

func TestCatalogRefreshKeepsConfiguredContext(t *testing.T) {
	for _, alias := range []bool{false, true} {
		m := compactCmdModel()
		key := "kimi-k3-fast"
		if alias {
			key = "custom-model"
			m.modelName = key
		}
		m.cfg.Models[key] = config.Model{ID: "kimi-k3-fast", Providers: []string{"inference"}, Context: 64000}
		if got := m.contextLimitFor("inference", "kimi-k3-fast"); got != 64000 {
			t.Fatalf("alias=%v: configured context = %d", alias, got)
		}
		m.updateCatalogs(map[string]config.Catalog{
			"inference": {Models: []config.ModelInfoLite{{ID: "kimi-k3-fast", ContextLength: 262144}}},
		})
		if m.agent.ContextLimit != 64000 {
			t.Fatalf("alias=%v: refreshed context = %d", alias, m.agent.ContextLimit)
		}
		m.updateCatalogs(map[string]config.Catalog{})
		if m.agent.ContextLimit != 64000 {
			t.Fatalf("alias=%v: empty catalog context = %d", alias, m.agent.ContextLimit)
		}
	}
}

func TestCompactBareKeepsSelection(t *testing.T) {
	m := compactCmdModel()
	m.compactModel, m.compactProv = "glm-5.2-fast", ""
	m.applyCompactModel()
	m.busy = true
	m.command("/compact")
	if m.compactModel != "glm-5.2-fast" || m.agent.CompactModel != "glm-5.2-fast" {
		t.Fatal("bare /compact must not change the compaction-model selection")
	}
}

func TestCompactThresholdFor(t *testing.T) {
	cases := []struct {
		pct  int
		want float64
	}{
		{0, 0.5},
		{70, 0.7},
		{5, 0.1},
		{99, 0.9},
		{-30, 0.1},
	}
	for _, tc := range cases {
		cfg := &config.Config{CompactPct: tc.pct}
		if got := compactThresholdFor(cfg); got != tc.want {
			t.Errorf("compactThresholdFor(%d) = %v, want %v", tc.pct, got, tc.want)
		}
	}
}

func TestSetCompactPct(t *testing.T) {
	m := compactCmdModel()
	m.agent.CompactThreshold = 0.5

	m.setCompactPct(60)
	if m.agent.CompactThreshold != 0.6 || m.cfg.CompactPct != 60 || m.compactPct() != 60 {
		t.Fatalf("setCompactPct(60): agent=%v cfg=%d", m.agent.CompactThreshold, m.cfg.CompactPct)
	}

	m.setCompactPct(120)
	if m.agent.CompactThreshold != 0.9 || m.cfg.CompactPct != 90 {
		t.Fatalf("setCompactPct(120) should clamp to 90: agent=%v cfg=%d", m.agent.CompactThreshold, m.cfg.CompactPct)
	}
	m.setCompactPct(0)
	if m.agent.CompactThreshold != 0.1 || m.cfg.CompactPct != 10 {
		t.Fatalf("setCompactPct(0) should clamp to 10: agent=%v cfg=%d", m.agent.CompactThreshold, m.cfg.CompactPct)
	}
}
