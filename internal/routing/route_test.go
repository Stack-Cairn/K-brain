package routing

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

func TestResolveRouteUsesConfiguredDefaultsAndLimits(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"id":"model1","context_length":12000,"max_output_tokens":2000}]}`))
	}))
	defer srv.Close()
	cfg := &config.Config{
		DefaultModel: "model1",
		Providers: map[string]config.Provider{
			"demo": {BaseURL: srv.URL + "/v1", API: "openai-completions", APIKey: "test"},
		},
		Models: map[string]config.Model{
			"model1": {Providers: []string{"demo"}, Context: 8000, MaxOut: 1000, Vision: true},
		},
	}
	route, err := ResolveRoute(cfg, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if route.ModelName != "model1" || route.ProviderName != "demo" || route.APIModel != "model1" {
		t.Fatalf("route identity = %+v", route)
	}
	if route.ContextLimit != 8000 || route.MaxOutput != 1000 {
		t.Fatalf("route limits = (%d, %d)", route.ContextLimit, route.MaxOutput)
	}
	if !route.Configured() {
		t.Fatal("configured route reported as unconfigured")
	}
	sub, err := SubModelFor(cfg, "model1", "demo")
	if err != nil || !route.Vision || !route.AgentModel().Vision || !sub.Vision {
		t.Fatalf("vision not propagated through route/model/task: %+v, %+v, %v", route, sub, err)
	}
}

func TestResolveRouteOptionalAllowsEmptyConfiguration(t *testing.T) {
	cfg := &config.Config{DefaultModel: "model1", Providers: map[string]config.Provider{
		"demo": {},
	}, Models: map[string]config.Model{
		"model1": {Providers: []string{"demo"}},
	}}
	route, err := ResolveRoute(cfg, "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if route.Client == nil || route.Configured() {
		t.Fatalf("optional route = %+v", route)
	}
}
