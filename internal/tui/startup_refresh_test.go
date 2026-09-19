package tui

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/routing"
)

func modelsServer(t *testing.T, hits *atomic.Int32, ids ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if hits != nil {
			hits.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"data":[`)
		for i, id := range ids {
			if i > 0 {
				fmt.Fprint(w, ",")
			}
			fmt.Fprintf(w, `{"id":%q,"context_length":1000000}`, id)
		}
		fmt.Fprintf(w, `]}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestBuildAgentWithRefreshRecoversFromMissingCatalog(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	var hits atomic.Int32
	srv := modelsServer(t, &hits, "kimi-k3")
	cfg := &config.Config{
		DefaultModel:    "kimi-k3",
		DefaultProvider: "demo",
		Providers: map[string]config.Provider{
			"demo": {BaseURL: srv.URL + "/v1", API: "openai-completions", APIKey: "k"},
		},
		Models: map[string]config.Model{},
	}

	ag, mn, pn, err := buildAgent(cfg, "", "", "")
	if err != nil {
		t.Fatalf("refresh-and-retry should recover the launch: %v", err)
	}
	if mn != "kimi-k3" || pn != "demo" {
		t.Errorf("route: %s@%s", mn, pn)
	}
	if ag == nil || ag.Model != "kimi-k3" {
		t.Errorf("agent should run the catalog id, got %+v", ag)
	}
	if ag.ContextLimit != 1000000 {
		t.Errorf("context limit should come from the refreshed catalog, got %d", ag.ContextLimit)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("want exactly 1 refresh fetch, got %d", n)
	}

	if _, _, _, err := cfg.Resolve("kimi-k3", ""); err != nil {
		t.Errorf("catalog should be cached after the refresh: %v", err)
	}
}

func TestBuildAgentWithRefreshSkipsFetchWhenResolveSucceeds(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	var hits atomic.Int32
	srv := modelsServer(t, &hits, "kimi-k3")
	cfg := &config.Config{
		DefaultModel:    "glm-5.2-fast",
		DefaultProvider: "demo",
		Providers: map[string]config.Provider{
			"demo": {BaseURL: srv.URL + "/v1", API: "openai-completions", APIKey: "k"},
		},
		Models: map[string]config.Model{
			"glm-5.2-fast": {Providers: []string{"demo"}},
		},
	}

	if _, _, _, err := buildAgent(cfg, "", "", ""); err != nil {
		t.Fatal(err)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("successful resolve must not fetch, got %d requests", n)
	}
}

func TestBuildAgentConfiguredContextOverridesCatalog(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	if err := config.SaveCatalogs(map[string]config.Catalog{
		"demo": {Models: []config.ModelInfoLite{{ID: "model1", ContextLength: 256000, MaxCompletionTokens: 16384}}},
	}); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	prov := cfg.Providers["demo"]
	prov.BaseURL, prov.APIKey = "http://localhost:1234/v1", "test-key"
	cfg.Providers["demo"] = prov
	ag, _, _, err := buildAgent(cfg, "model1", "demo", "test")
	if err != nil {
		t.Fatal(err)
	}
	if ag.ContextLimit != 128000 || ag.MaxTokens != 8192 {
		t.Fatalf("configured limits = (%d, %d)", ag.ContextLimit, ag.MaxTokens)
	}
}

func TestBuildAgentWithRefreshStillErrorsForUnknownModel(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	var hits atomic.Int32
	srv := modelsServer(t, &hits, "kimi-k3")
	cfg := &config.Config{
		DefaultModel:    "nope",
		DefaultProvider: "demo",
		Providers: map[string]config.Provider{
			"demo": {BaseURL: srv.URL + "/v1", API: "openai-completions", APIKey: "k"},
		},
		Models: map[string]config.Model{},
	}

	_, _, _, err := buildAgent(cfg, "", "", "")
	_, ok := errors.AsType[*config.UnknownModelError](err)
	if !ok {
		t.Fatalf("persistent miss should surface the original typed error, got %T (%v)", err, err)
	}
	if !strings.Contains(err.Error(), `unknown model "nope"`) {
		t.Errorf("original message lost: %v", err)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("want exactly 1 refresh attempt, got %d", n)
	}
}

func TestResolveWithRefreshRecoversFromMissingCatalog(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	var hits atomic.Int32
	srv := modelsServer(t, &hits, "kimi-k3")
	cfg := &config.Config{
		DefaultModel:    "kimi-k3",
		DefaultProvider: "demo",
		Providers: map[string]config.Provider{
			"demo": {BaseURL: srv.URL + "/v1", API: "openai-completions", APIKey: "k"},
		},
		Models: map[string]config.Model{},
	}

	prov, mdl, id, err := routing.ResolveWithRefresh(cfg, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if id != "kimi-k3" || prov.BaseURL != srv.URL+"/v1" {
		t.Errorf("route: id=%q base=%q", id, prov.BaseURL)
	}
	if mdl.Context != 1000000 {
		t.Errorf("synthetic model should carry catalog context, got %+v", mdl)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("want exactly 1 refresh fetch, got %d", n)
	}
}

func TestBuildAgentCatalogFailureIsNotRepeated(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer key" {
			t.Errorf("unexpected catalog request: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	cfg := &config.Config{
		DefaultModel: "missing", DefaultProvider: "demo",
		Providers: map[string]config.Provider{
			"demo": {BaseURL: srv.URL + "/v1", API: "openai-completions", APIKey: "key"},
		},
	}
	_, _, _, err := buildAgent(cfg, "", "", "")
	if _, ok := errors.AsType[*config.UnknownModelError](err); !ok {
		t.Fatalf("expected original unknown model error, got %v", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("failed catalog request repeated %d times", hits.Load())
	}
}
