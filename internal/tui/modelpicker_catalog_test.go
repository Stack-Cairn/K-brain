package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

func pickerCfg() *config.Config {
	return &config.Config{
		DefaultModel: "kimi-k3-fast",
		Providers:    map[string]config.Provider{"inference": {BaseURL: "https://api.example.com/v1"}},
		Models: map[string]config.Model{
			"kimi-k3-fast": {Providers: []string{"inference"}},
		},
	}
}

func TestBuildModelItemsAppendsCatalogRoutes(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	cfg := pickerCfg()
	if err := config.SaveCatalogs(map[string]config.Catalog{
		"inference": {
			FetchedAt: time.Now(),
			BaseURL:   "https://api.example.com/v1",
			Models: []config.ModelInfoLite{
				{ID: "kimi-k3-fast", ContextLength: 1048576},
				{ID: "deepseek-v4-pro", ContextLength: 1048576},
				{ID: "glm-5.2", ContextLength: 1000000},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	items := buildModelItems(cfg)
	if len(items) != 3 {
		t.Fatalf("want 1 configured + 2 catalog routes, got %d: %+v", len(items), items)
	}
	if items[0].model != "kimi-k3-fast" || items[0].fromCatalog {
		t.Errorf("configured route first, unmarked: %+v", items[0])
	}
	if items[1].model != "deepseek-v4-pro" || !items[1].fromCatalog {
		t.Errorf("catalog routes sorted after configured: %+v", items[1])
	}
	if items[2].model != "glm-5.2" || !items[2].fromCatalog || items[2].provider != "inference" {
		t.Errorf("catalog route should carry its provider: %+v", items[2])
	}
}

func TestModelPickerViewMarksCatalogAndStale(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	cfg := pickerCfg()
	if err := config.SaveCatalogs(map[string]config.Catalog{
		"inference": {
			FetchedAt: time.Now().Add(-48 * time.Hour),
			BaseURL:   "https://api.example.com/v1",
			Models:    []config.ModelInfoLite{{ID: "deepseek-v4-pro"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	m := &model{cfg: cfg, modelName: "kimi-k3-fast", provName: "inference"}
	m.openModelPicker(false)
	if m.mpicker == nil {
		t.Fatal("picker should open")
	}
	view := m.modelPickerView()
	if !strings.Contains(view, "(new)") {
		t.Error("catalog routes should carry a (new) marker")
	}
	if !strings.Contains(view, "deepseek-v4-pro") {
		t.Error("catalog model should be listed")
	}
	if !strings.Contains(view, "stale") || !strings.Contains(view, "/model refresh") {
		t.Error("stale catalog should hint at /model refresh")
	}
}

func TestModelPickerSelectsCatalogRoute(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	if err := config.SaveCatalogs(map[string]config.Catalog{
		"inference": {
			FetchedAt: time.Now(),
			BaseURL:   "https://x",
			Models:    []config.ModelInfoLite{{ID: "deepseek-v4-pro", ContextLength: 1048576}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	m := compactCmdModel()
	m.openModelPicker(false)
	p := m.mpicker

	for p.idx < len(p.items)-1 && !p.items[p.idx].fromCatalog {
		p.idx++
	}
	if !p.items[p.idx].fromCatalog {
		t.Fatal("no catalog route in picker")
	}
	tm, _ := m.modelPickerKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = tm.(*model)
	if m.modelName != "deepseek-v4-pro" || m.provName != "inference" {
		t.Fatalf("enter on a catalog route should switch models, got %s @ %s", m.modelName, m.provName)
	}
	if m.cfg.DefaultModel != "deepseek-v4-pro" {
		t.Errorf("the switch should persist as the new default, got %q", m.cfg.DefaultModel)
	}
	if _, ok := m.cfg.Models["deepseek-v4-pro"]; ok {
		t.Error("catalog routes must not be written into cfg.Models")
	}
}

func TestModelRefreshForcesRefetch(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	if err := config.SaveCatalogs(map[string]config.Catalog{
		"inference": {FetchedAt: time.Now(), BaseURL: "https://x", Models: []config.ModelInfoLite{{ID: "seed"}}},
	}); err != nil {
		t.Fatal(err)
	}
	m := modelCmdModel()
	m = typeStr(t, m, "/model refresh")
	tm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = tm.(*model)
	if m.mpicker != nil {
		t.Fatal("/model refresh must not open the picker")
	}
	if len(m.blocks) == 0 || !strings.Contains(m.blocks[len(m.blocks)-1].text, "refreshing model catalogs") {
		t.Fatalf("refresh notice should be appended, got %+v", m.blocks)
	}
	got := config.LoadCatalogs()["inference"]
	if len(got.Models) != 1 || got.Models[0].ID != "seed" {
		t.Errorf("failed refetch must keep the existing cache, got %+v", got)
	}
}

func TestModelRefreshCompletion(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	if err := config.SaveCatalogs(map[string]config.Catalog{
		"inference": {
			FetchedAt: time.Now(), BaseURL: "https://x",
			Models: []config.ModelInfoLite{{ID: "catalog-only-model"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	m := modelCmdModel()
	_, cs := completions("/model r", m.modelCands(), m.providerCands(), nil, nil)
	if len(cs) != 1 || cs[0].Text != "refresh" {
		t.Fatalf("refresh should complete under /model, got %+v", cs)
	}
	_, cs = completions("/model cat", m.modelCands(), m.providerCands(), nil, nil)
	if len(cs) != 1 || cs[0].Text != "catalog-only-model" {
		t.Fatalf("catalog-only models should complete under /model, got %+v", cs)
	}

	_, cs = completions("/model refresh inf", m.modelCands(), m.providerCands(), nil, nil)
	if len(cs) != 0 {
		t.Errorf("refresh takes no provider argument, got %+v", cs)
	}
}

func TestStaleCatalogs(t *testing.T) {
	cfg := pickerCfg()
	if got := staleCatalogs(cfg, map[string]config.Catalog{}); len(got) != 1 || got[0] != "inference" {
		t.Errorf("missing catalog is stale: %v", got)
	}
	fresh := map[string]config.Catalog{"inference": {FetchedAt: time.Now()}}
	if got := staleCatalogs(cfg, fresh); len(got) != 0 {
		t.Errorf("fresh catalog is not stale: %v", got)
	}
}

func TestBuildModelItemsGroupsCatalogRoutesByProvider(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	cfg := &config.Config{
		Providers: map[string]config.Provider{"gateway": {}, "custom": {}},
		Models:    map[string]config.Model{},
	}
	if err := config.SaveCatalogs(map[string]config.Catalog{
		"gateway": {FetchedAt: time.Now(), Models: []config.ModelInfoLite{{ID: "a"}, {ID: "z"}}},
		"custom":  {FetchedAt: time.Now(), Models: []config.ModelInfoLite{{ID: "m"}}},
	}); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range buildModelItems(cfg) {
		got = append(got, it.model+"@"+it.provider)
	}
	want := "m@custom a@gateway z@gateway"
	if strings.Join(got, " ") != want {
		t.Fatalf("order = %v, want %s", got, want)
	}
}

func TestEndpointLabelShowsCustomEndpoint(t *testing.T) {
	url := "https://api.example.com/v1"
	if got := endpointLabel(url); got != url {
		t.Fatalf("endpoint = %q, want %q", got, url)
	}
}

func TestModelPickerKeepsSelectionVisibleWhenScrolling(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	cfg := &config.Config{Providers: map[string]config.Provider{"p": {}}, Models: map[string]config.Model{}}
	var many []config.ModelInfoLite
	for i := range 300 {
		many = append(many, config.ModelInfoLite{ID: fmt.Sprintf("vendor/model-%03d", i)})
	}
	if err := config.SaveCatalogs(map[string]config.Catalog{"p": {FetchedAt: time.Now(), Models: many}}); err != nil {
		t.Fatal(err)
	}
	m := &model{cfg: cfg, height: 30, width: 100}
	m.openModelPicker(false)
	for range 150 {
		m.mpicker.idx++
	}
	view := m.modelPickerView()
	lines := strings.Split(view, "\n")
	if len(lines) > 29 {
		t.Fatalf("picker should fit the terminal, got %d lines", len(lines))
	}
	if !strings.Contains(view, "→") || !strings.Contains(view, "vendor/model-150") {
		t.Fatalf("selection must stay visible after scrolling:\n%s", view)
	}
	if !strings.Contains(lines[0], "/") || !strings.Contains(view, "type to filter") {
		t.Fatalf("query line and footer must stay visible:\n%s", view)
	}
}
