package routing

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

func TestClientForProviderCustomEndpoint(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/custom/v1/models" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("request = %s, authorization = %q", r.URL.Path, r.Header.Get("Authorization"))
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"model1"}]}`)
	}))
	defer srv.Close()
	provider := config.Provider{API: "openai-completions", BaseURL: srv.URL + "/custom/v1", APIKey: "test-key"}
	client, err := ClientForProvider(provider, "custom", 1)
	if err != nil {
		t.Fatal(err)
	}
	models, err := client.Models(context.Background())
	if err != nil || len(models) != 1 || models[0].ID != "model1" {
		t.Fatalf("models = %+v, err = %v", models, err)
	}
	cats := RefreshCatalogs(&config.Config{Providers: map[string]config.Provider{"custom": provider}}, true)
	if cats["custom"].Find("model1") == nil {
		t.Fatalf("catalog = %+v", cats)
	}
}

func TestClientForProviderRequiresEndpointAndKey(t *testing.T) {
	for name, provider := range map[string]config.Provider{
		"no endpoint":        {APIKey: "key"},
		"relative endpoint":  {BaseURL: "/v1", APIKey: "key"},
		"unsupported scheme": {BaseURL: "file:///tmp/api", APIKey: "key"},
		"no key":             {BaseURL: "https://api.example.com/v1"},
		"blank key":          {BaseURL: "https://api.example.com/v1", APIKey: " "},
		"subscription api":   {API: "openai-codex-responses", BaseURL: "https://api.example.com/v1", APIKey: "key"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ClientForProvider(provider, name, 1); err == nil {
				t.Fatal("expected invalid provider to be rejected")
			}
		})
	}
}

func TestClientForProviderOptionalAllowsUnconfiguredProvider(t *testing.T) {
	for _, p := range []config.Provider{{}, {BaseURL: "https://example.com"}, {APIKey: "key"}} {
		if client, err := ClientForProviderOptional(p, "demo", 0); err != nil || client == nil {
			t.Fatalf("optional client for %+v = %v, %v", p, client, err)
		}
	}
}
