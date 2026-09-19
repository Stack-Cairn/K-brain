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

func TestResolveRouteCancellationStopsCatalogRefresh(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	defer srv.Close()
	cfg := &config.Config{DefaultModel: "missing", Providers: map[string]config.Provider{
		"a": {BaseURL: srv.URL + "/v1", APIKey: "key"},
		"b": {BaseURL: srv.URL + "/v1", APIKey: "key"},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := ResolveRouteContext(ctx, cfg, "", "", false); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline instead of unknown model: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("catalog outlived caller deadline: %s", elapsed)
	}
	if calls.Load() != 1 {
		t.Fatalf("refresh continued to other providers after cancellation: %d", calls.Load())
	}
	if len(config.LoadCatalogs()) != 0 {
		t.Fatal("canceled request wrote a catalog")
	}
	if _, err := ResolveRouteContext(ctx, cfg, "", "", false); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired context: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatal("expired context initiated another request")
	}
}
