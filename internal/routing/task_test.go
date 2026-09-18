package routing

import (
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

func taskCfg(t *testing.T, url string) *config.Config {
	t.Helper()
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	return &config.Config{
		DefaultModel: "m",
		Providers:    map[string]config.Provider{"p": {BaseURL: url, APIKey: "k"}},
		Models: map[string]config.Model{
			"m":                     {Providers: []string{"p"}},
			config.DefaultTaskModel: {Providers: []string{"p"}, Context: 384000},
		},
	}
}

func TestTaskDefaultForResolvesDefault(t *testing.T) {
	o, err := TaskDefaultFor(taskCfg(t, "http://x"), "")
	if err != nil || o.Client == nil || o.Model != config.DefaultTaskModel {
		t.Fatalf("default should resolve: %+v, %v", o, err)
	}
	if o.ContextLimit != 384000 {
		t.Fatalf("context should carry over, got %d", o.ContextLimit)
	}
}

func TestTaskDefaultForFallbacks(t *testing.T) {
	cfg := taskCfg(t, "http://x")
	delete(cfg.Models, config.DefaultTaskModel)
	o, err := TaskDefaultFor(cfg, "")
	if err != nil || o.Client != nil {
		t.Fatalf("missing default must silently fall back, got %+v, %v", o, err)
	}
	cfg.TaskModel = "nope"
	if _, err := TaskDefaultFor(cfg, ""); err == nil {
		t.Fatal("an explicit taskModel that fails to resolve should error")
	}
}

func TestTaskDefaultForCatalogSuffix(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	cfg := &config.Config{
		DefaultModel: "m",
		Providers:    map[string]config.Provider{"gateway": {BaseURL: "http://x", APIKey: "k"}},
		Models:       map[string]config.Model{"m": {Providers: []string{"gateway"}}},
	}
	if err := config.SaveCatalogs(map[string]config.Catalog{
		"gateway": {FetchedAt: time.Now(), Models: []config.ModelInfoLite{
			{ID: "deepseek/" + config.DefaultTaskModel, ContextLength: 128000},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	o, err := TaskDefaultFor(cfg, "")
	if err != nil || o.Client == nil || o.Model != "deepseek/"+config.DefaultTaskModel {
		t.Fatalf("suffix scan should resolve the prefixed catalog id: %+v, %v", o, err)
	}
}
