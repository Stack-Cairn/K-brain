package routing

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
)

type failoverScript struct {
	stream func(context.Context, ai.Request, func(string), func(string), func(string, string, string)) (ai.Message, ai.Usage, error)
	calls  atomic.Int32
}

func (c *failoverScript) Models(context.Context) ([]ai.ModelInfo, error) { return nil, nil }
func (c *failoverScript) Complete(context.Context, ai.Request) (string, ai.Usage, error) {
	return "", ai.Usage{}, errors.New("not used")
}
func (c *failoverScript) Clone() ai.Client   { return c }
func (c *failoverScript) SetCacheKey(string) {}
func (c *failoverScript) Endpoint() string   { return "http://fixture.invalid" }
func (c *failoverScript) Stream(ctx context.Context, req ai.Request, onText, onThink func(string), onTool func(string, string, string)) (ai.Message, ai.Usage, error) {
	c.calls.Add(1)
	return c.stream(ctx, req, onText, onThink, onTool)
}

func testCandidate(name string, client ai.Client, maxSwitches int) failoverCandidate {
	settings := DefaultFailoverSettings()
	settings.Failover.MaxSwitches = maxSwitches
	settings.Failover.Cooldown = time.Hour
	return failoverCandidate{name: name, client: client, settings: settings}
}

func TestFailoverDiscardsPreCommitHTTPFailure(t *testing.T) {
	ResetFailoverBreakers()
	first := &failoverScript{stream: func(context.Context, ai.Request, func(string), func(string), func(string, string, string)) (ai.Message, ai.Usage, error) {
		return ai.Message{}, ai.Usage{}, &ai.HTTPError{Status: "503 Service Unavailable", StatusCode: http.StatusServiceUnavailable}
	}}
	second := &failoverScript{stream: func(_ context.Context, _ ai.Request, onText, _ func(string), _ func(string, string, string)) (ai.Message, ai.Usage, error) {
		onText("fallback")
		return ai.Message{Role: "assistant", Content: "fallback", StopReason: ai.StopReasonStop}, ai.Usage{}, nil
	}}
	client := &FailoverClient{candidates: []failoverCandidate{testCandidate("primary", first, 1), testCandidate("fallback", second, 1)}, maxSwitches: 1}
	var text string
	message, _, err := client.Stream(t.Context(), ai.Request{Model: "model"}, func(delta string) { text += delta }, nil, nil)
	if err != nil || message.Content != "fallback" || text != "fallback" {
		t.Fatalf("fallback result = message=%+v text=%q err=%v", message, text, err)
	}
	if first.calls.Load() != 1 || second.calls.Load() != 1 {
		t.Fatalf("calls = primary %d fallback %d", first.calls.Load(), second.calls.Load())
	}
}

func TestFailoverDoesNotRepeatAfterCommittedDelta(t *testing.T) {
	ResetFailoverBreakers()
	first := &failoverScript{stream: func(_ context.Context, _ ai.Request, onText, _ func(string), _ func(string, string, string)) (ai.Message, ai.Usage, error) {
		onText("partial")
		return ai.Message{Role: "assistant", Content: "partial"}, ai.Usage{}, &ai.HTTPError{Status: "503 Service Unavailable", StatusCode: http.StatusServiceUnavailable}
	}}
	second := &failoverScript{stream: func(_ context.Context, _ ai.Request, onText, _ func(string), _ func(string, string, string)) (ai.Message, ai.Usage, error) {
		onText("duplicate")
		return ai.Message{Role: "assistant", Content: "duplicate"}, ai.Usage{}, nil
	}}
	client := &FailoverClient{candidates: []failoverCandidate{testCandidate("primary", first, 1), testCandidate("fallback", second, 1)}, maxSwitches: 1}
	var text string
	_, _, err := client.Stream(t.Context(), ai.Request{Model: "model"}, func(delta string) { text += delta }, nil, nil)
	if err == nil || text != "partial" || second.calls.Load() != 0 {
		t.Fatalf("committed failure = text=%q err=%v fallbackCalls=%d", text, err, second.calls.Load())
	}
}

func TestFailoverCancellationDoesNotStartFallback(t *testing.T) {
	ResetFailoverBreakers()
	first := &failoverScript{stream: func(ctx context.Context, _ ai.Request, _ func(string), _ func(string), _ func(string, string, string)) (ai.Message, ai.Usage, error) {
		<-ctx.Done()
		return ai.Message{}, ai.Usage{}, ctx.Err()
	}}
	second := &failoverScript{stream: func(_ context.Context, _ ai.Request, _ func(string), _ func(string), _ func(string, string, string)) (ai.Message, ai.Usage, error) {
		return ai.Message{}, ai.Usage{}, nil
	}}
	ctx, cancel := context.WithCancel(t.Context())
	client := &FailoverClient{candidates: []failoverCandidate{testCandidate("primary", first, 1), testCandidate("fallback", second, 1)}, maxSwitches: 1}
	done := make(chan error, 1)
	go func() {
		_, _, err := client.Stream(ctx, ai.Request{Model: "model"}, nil, nil, nil)
		done <- err
	}()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) || second.calls.Load() != 0 {
		t.Fatalf("cancel = err=%v fallbackCalls=%d", err, second.calls.Load())
	}
}

func TestFailoverCircuitSkipsRepeatedlyFailingPrimary(t *testing.T) {
	ResetFailoverBreakers()
	first := &failoverScript{stream: func(context.Context, ai.Request, func(string), func(string), func(string, string, string)) (ai.Message, ai.Usage, error) {
		return ai.Message{}, ai.Usage{}, &ai.HTTPError{Status: "503 Service Unavailable", StatusCode: http.StatusServiceUnavailable}
	}}
	second := &failoverScript{stream: func(_ context.Context, _ ai.Request, onText, _ func(string), _ func(string, string, string)) (ai.Message, ai.Usage, error) {
		onText("healthy")
		return ai.Message{Role: "assistant", Content: "healthy", StopReason: ai.StopReasonStop}, ai.Usage{}, nil
	}}
	primary := testCandidate("primary", first, 1)
	primary.settings.Failover.FailureThreshold = 1
	client := &FailoverClient{candidates: []failoverCandidate{primary, testCandidate("fallback", second, 1)}, maxSwitches: 1}
	for i := 0; i < 2; i++ {
		_, _, err := client.Stream(t.Context(), ai.Request{Model: "model"}, nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	if first.calls.Load() != 1 || second.calls.Load() != 2 {
		t.Fatalf("breaker calls = primary %d fallback %d", first.calls.Load(), second.calls.Load())
	}
}

func TestFailoverHTTPFaultFallsBackAndKeepsSameVendorQueue(t *testing.T) {
	ResetFailoverBreakers()
	var primaryCalls, fallbackCalls atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		primaryCalls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = fmt.Fprint(w, `{"error":{"message":"temporary"}}`)
	}))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fallbackCalls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer fallback.Close()
	cfg := &config.Config{
		DefaultModel: "model",
		Providers: map[string]config.Provider{
			"primary":  {API: ai.APIChatCompletions, BaseURL: primary.URL, APIKey: "key", RetryPolicy: map[string]any{"mode": "off", "failover": map[string]any{"maxSwitches": 1}}},
			"fallback": {API: ai.APIChatCompletions, BaseURL: fallback.URL, APIKey: "key", RetryPolicy: map[string]any{"mode": "off", "failover": map[string]any{"maxSwitches": 1}}},
			"foreign":  {API: ai.APIResponses, BaseURL: fallback.URL, APIKey: "key"},
		},
		Models: map[string]config.Model{"model": {ID: "model", Context: 200000, Providers: []string{"primary", "fallback", "foreign"}}},
	}
	route, err := ResolveFailoverRouteContext(t.Context(), cfg, "model", "primary", false)
	if err != nil {
		t.Fatal(err)
	}
	client, ok := route.Client.(*FailoverClient)
	if !ok || len(client.candidates) != 2 || client.candidates[0].name != "primary" || client.candidates[1].name != "fallback" {
		t.Fatalf("candidate queue = %#v", client)
	}
	var text string
	message, _, err := route.Client.Stream(t.Context(), ai.Request{Model: "model"}, func(delta string) { text += delta }, nil, nil)
	if err != nil || message.Content != "ok" || text != "ok" {
		t.Fatalf("HTTP fallback = message=%+v text=%q err=%v", message, text, err)
	}
	if primaryCalls.Load() != 1 || fallbackCalls.Load() != 1 {
		t.Fatalf("HTTP calls = primary %d fallback %d", primaryCalls.Load(), fallbackCalls.Load())
	}
}

func TestProviderRetryPolicyAttemptsMatchLiveAgent(t *testing.T) {
	cases := []struct {
		name   string
		policy RetryPolicy
		want   int
	}{
		{"default", RetryPolicy{Mode: "default"}, 6},
		{"off", RetryPolicy{Mode: "off", MaxRetries: 20}, 1},
		{"custom", RetryPolicy{Mode: "custom", MaxRetries: 2}, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := providerAttempts(tc.policy); got != tc.want {
				t.Fatalf("attempts = %d, want %d", got, tc.want)
			}
		})
	}
}
