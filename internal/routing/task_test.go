package routing

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
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
			"m":      {Providers: []string{"p"}},
			"worker": {Providers: []string{"p"}, Context: 384000},
		},
	}
}

func TestTaskDefaultInheritsWithoutCatalogRequest(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "unexpected catalog request", 500)
	}))
	defer srv.Close()
	cfg := taskCfg(t, srv.URL+"/v1")
	if err := config.SaveCatalogs(map[string]config.Catalog{
		"p": {FetchedAt: time.Now(), Models: []config.ModelInfoLite{{ID: "vendor/model1"}}},
	}); err != nil {
		t.Fatal(err)
	}
	cfg.Models["model1"] = config.Model{Providers: []string{"p"}}
	o, err := TaskDefaultFor(cfg)
	if err != nil || o.Client != nil || o.Model != "" || calls.Load() != 0 {
		t.Fatalf("unset taskModel must inherit, not select a placeholder or fetch: %+v, %v; calls=%d", o, err, calls.Load())
	}
}

func TestTaskDefaultExplicitModelAndProvider(t *testing.T) {
	cfg := taskCfg(t, "https://example.test/v1")
	cfg.TaskModel = "worker"
	cfg.TaskProvider = "p"
	o, err := TaskDefaultFor(cfg)
	if err != nil || o.Client == nil || o.Model != "worker" || o.Provider != "p" || o.ContextLimit != 384000 {
		t.Fatalf("explicit task model lost route or limits: %+v, %v", o, err)
	}
	cfg.TaskProvider = "missing"
	if _, err := TaskDefaultFor(cfg); err == nil {
		t.Fatal("unknown explicit task provider must fail")
	}
}

func TestTaskDefaultExplicitCatalogModel(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" || r.URL.Path != "/v1/models" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"id":"vendor/worker","context_length":128000}]}`))
	}))
	defer srv.Close()
	cfg := taskCfg(t, srv.URL+"/v1")
	cfg.TaskModel = "vendor/worker"
	o, err := TaskDefaultFor(cfg)
	if err != nil || o.Model != "vendor/worker" || o.ContextLimit != 128000 || calls.Load() != 1 {
		t.Fatalf("explicit catalog route: %+v, %v; calls=%d", o, err, calls.Load())
	}
	cfg.TaskModel = "absent"
	if _, err := TaskDefaultFor(cfg); err == nil {
		t.Fatal("unknown explicit task model must fail")
	}
}

func TestTaskDefaultInvalidContextAndConfig(t *testing.T) {
	if _, err := TaskDefaultFor(nil); err == nil {
		t.Fatal("nil configuration must fail")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := TaskDefaultForContext(ctx, &config.Config{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}
