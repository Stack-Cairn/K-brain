package routing

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
)

func TestFailoverRetryPolicyUsesLiveAgentTotalAttemptCount(t *testing.T) {
	ResetFailoverBreakers()
	var primaryCalls, fallbackCalls atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		primaryCalls.Add(1)
		w.Header().Set("Retry-After-Ms", "0")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = fmt.Fprint(w, `{"error":{"message":"temporary"}}`)
	}))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fallbackCalls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"recovered\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer fallback.Close()

	policy := map[string]any{"mode": "custom", "maxRetries": 2, "failover": map[string]any{"maxSwitches": 1}}
	cfg := &config.Config{
		DefaultModel: "model",
		Providers: map[string]config.Provider{
			"primary":  {API: ai.APIChatCompletions, BaseURL: primary.URL, APIKey: "key", RetryPolicy: policy},
			"fallback": {API: ai.APIChatCompletions, BaseURL: fallback.URL, APIKey: "key", RetryPolicy: map[string]any{"mode": "off", "failover": map[string]any{"maxSwitches": 1}}},
		},
		Models: map[string]config.Model{"model": {ID: "model", Context: 200000, Providers: []string{"primary", "fallback"}}},
	}
	route, err := ResolveFailoverRouteContext(t.Context(), cfg, "model", "primary", false)
	if err != nil {
		t.Fatal(err)
	}
	var text string
	message, _, err := route.Client.Stream(t.Context(), ai.Request{Model: "model"}, func(delta string) { text += delta }, nil, nil)
	if err != nil || message.Content != "recovered" || text != "recovered" {
		t.Fatalf("result = message=%+v text=%q err=%v", message, text, err)
	}
	if primaryCalls.Load() != 3 || fallbackCalls.Load() != 1 {
		t.Fatalf("calls = primary %d fallback %d, want 3 and 1", primaryCalls.Load(), fallbackCalls.Load())
	}
}
