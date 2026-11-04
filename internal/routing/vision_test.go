package routing

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

func TestSupportsVisionStoredModalitiesBeatCatalog(t *testing.T) {
	catalogs := map[string]config.Catalog{"relay": {Models: []config.ModelInfoLite{
		{ID: "flash", InputModalities: []string{"text"}},
		{ID: "pro", InputModalities: []string{"text", "image"}},
		{ID: "bare"},
	}}}
	cfg := &config.Config{Models: map[string]config.Model{
		// The model editor's "text and image" override on a relay that lists the model as text-only.
		"flash": {Providers: []string{"relay"}, InputModalities: []string{"text", "image"}, Vision: true},
		// "Text only" set against a catalog that advertises images.
		"pro": {Providers: []string{"relay"}, InputModalities: []string{"text"}},
		// A legacy vision flag without stored modalities still yields to the catalog.
		"legacy": {Providers: []string{"relay"}, Vision: true},
		"bare":   {Providers: []string{"relay"}, Vision: true},
	}}
	cases := []struct {
		name, model, id string
		want            bool
	}{
		{"stored text+image over text-only catalog", "flash", "flash", true},
		{"stored text-only over image catalog", "pro", "pro", false},
		{"legacy flag yields to text-only catalog", "legacy", "flash", false},
		{"catalog without modalities falls back to flag", "bare", "bare", true},
		{"unknown model", "ghost", "ghost", false},
	}
	for _, c := range cases {
		if got := SupportsVision(cfg, c.model, c.id, catalogs, "relay"); got != c.want {
			t.Errorf("%s: SupportsVision = %v, want %v", c.name, got, c.want)
		}
	}
}

// The configuration file the desktop host writes after the model editor saves
// "text and image": the route must keep vision even though the cached /models
// catalog for that provider lists the model as text-only.
func TestResolveRouteKeepsStoredVisionOverTextOnlyCatalog(t *testing.T) {
	home := t.TempDir()
	t.Setenv("K_BRAIN_HOME", home)
	cfgJSON := `{"defaultModel":"deepseek-flash","defaultProvider":"deepseek","providers":{"deepseek":{"api":"openai-completions","baseUrl":"http://127.0.0.1:1/v1","apiKey":"k","models":[{"id":"deepseek-flash","inputModalities":["text","image"]}]}}}`
	path := filepath.Join(home, "config.json")
	if err := os.WriteFile(path, []byte(cfgJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveCatalogs(map[string]config.Catalog{"deepseek": {Models: []config.ModelInfoLite{
		{ID: "deepseek-flash", InputModalities: []string{"text"}},
	}}}); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	route, err := ResolveRouteContext(t.Context(), cfg, "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if !route.Vision || !route.AgentModel().Vision {
		t.Fatalf("route vision = %v, want true", route.Vision)
	}
}
