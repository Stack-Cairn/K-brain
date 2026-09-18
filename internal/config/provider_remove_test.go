package config

import "testing"

func TestRemoveProviderDropsRoutesPinsAndCatalog(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	if err := SaveCatalogs(map[string]Catalog{"provider1": {Models: []ModelInfoLite{{ID: "gpt-5.5"}}}, "provider2": {}}); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{
		DefaultProvider: "provider1",
		CompactProvider: "provider1",
		TaskProvider:    "provider2",
		Providers:       map[string]Provider{"provider1": {}, "provider2": {}},
		Models: map[string]Model{
			"gpt-5.5": {Providers: []string{"provider1"}},
			"shared":  {Providers: []string{"provider1", "provider2"}},
		},
	}
	cfg.RemoveProvider("provider1")
	if _, ok := cfg.Providers["provider1"]; ok {
		t.Fatal("provider entry should be gone")
	}
	if _, ok := cfg.Models["gpt-5.5"]; ok {
		t.Fatal("route with no remaining provider should be dropped")
	}
	if got := cfg.Models["shared"].Providers; len(got) != 1 || got[0] != "provider2" {
		t.Fatalf("shared route providers = %v", got)
	}
	if cfg.DefaultProvider != "" || cfg.CompactProvider != "" || cfg.TaskProvider != "provider2" {
		t.Fatalf("pins: default=%q compact=%q task=%q", cfg.DefaultProvider, cfg.CompactProvider, cfg.TaskProvider)
	}
	cats := LoadCatalogs()
	if _, ok := cats["provider1"]; ok || len(cats) != 1 {
		t.Fatalf("catalogs after remove = %v", cats)
	}
}

func TestRemoveLastProviderSavesAndClearsModelPins(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Providers = map[string]Provider{"only": {API: "openai-completions"}}
	cfg.Models = map[string]Model{"m": {Providers: []string{"only"}}}
	cfg.DefaultModel, cfg.CompactModel, cfg.TaskModel = "m", "m", "m"
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	cfg.RemoveProvider("only")
	if cfg.DefaultModel != "" || cfg.CompactModel != "" || cfg.TaskModel != "" {
		t.Fatalf("model pins should be cleared: %q %q %q", cfg.DefaultModel, cfg.CompactModel, cfg.TaskModel)
	}
	if err := cfg.Save(); err != nil {
		t.Fatalf("last-provider logout must persist: %v", err)
	}
	again, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := again.Providers["only"]; ok {
		t.Fatalf("removed provider came back from the backup on reload: %v", again.Providers)
	}

	again.Providers = map[string]Provider{"p": {}}
	if err := again.Save(); err != nil {
		t.Fatal(err)
	}
	if err := (&Config{}).Save(); err == nil {
		t.Fatal("an empty config not produced by RemoveProvider must still be refused")
	}
}
