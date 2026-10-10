package routing

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
)

func TestFailoverUsesDestinationMaxOutput(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			ResetFailoverBreakers()
			primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
				fmt.Fprint(w, `{"error":{"message":"temporary"}}`)
			}))
			defer primary.Close()
			var received int
			fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Stream              bool `json:"stream"`
					MaxTokens           int  `json:"max_tokens"`
					MaxCompletionTokens int  `json:"max_completion_tokens"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				received = max(body.MaxTokens, body.MaxCompletionTokens)
				if received > 32000 {
					w.WriteHeader(http.StatusBadRequest)
					fmt.Fprint(w, `{"error":{"message":"max_tokens exceeds 32000"}}`)
					return
				}
				if !body.Stream {
					fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
			}))
			defer fallback.Close()
			policy := map[string]any{"mode": "off", "failover": map[string]any{"maxSwitches": 1}}
			model := config.Model{ID: "shared", Providers: []string{"a", "b"}, Context: 200000, MaxOut: 128000}
			model.SetProviderMetadata("b", config.Model{Context: 200000, MaxOut: 32000})
			cfg := &config.Config{DefaultModel: "shared", Providers: map[string]config.Provider{
				"a": {API: ai.APIChatCompletions, BaseURL: primary.URL, APIKey: "fixture", RetryPolicy: policy},
				"b": {API: ai.APIChatCompletions, BaseURL: fallback.URL, APIKey: "fixture", RetryPolicy: policy},
			}, Models: map[string]config.Model{"shared": model}}
			route, err := ResolveFailoverRouteContext(t.Context(), cfg, "shared", "a", false)
			if err != nil {
				t.Fatal(err)
			}
			req := ai.Request{Model: "shared", MaxTokens: route.MaxOutput}
			if streaming {
				_, _, err = route.Client.Stream(t.Context(), req, nil, nil, nil)
			} else {
				_, _, err = route.Client.Complete(t.Context(), req)
			}
			if req.MaxTokens != 128000 {
				t.Fatal("original request changed")
			}
			if received != 32000 || err != nil {
				t.Fatalf("fallback received max_tokens=%d (limit 32000), error=%v", received, err)
			}
		})
	}
}

func TestFailoverExcludesNarrowerOrIncompatibleCandidates(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, tc := range []struct {
		name       string
		context    int
		modalities []string
		want       int
	}{
		{"compatible", 200000, []string{"text", "image"}, 2},
		{"smaller context", 64000, []string{"text", "image"}, 1},
		{"unknown context", 0, []string{"text", "image"}, 1},
		{"text only", 200000, []string{"text"}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := config.Model{ID: "shared", Providers: []string{"a", "b"}, Context: 200000, MaxOut: 128000, Vision: true, InputModalities: []string{"text", "image"}}
			model.SetProviderMetadata("b", config.Model{Context: tc.context, MaxOut: 32000, InputModalities: tc.modalities})
			cfg := &config.Config{Models: map[string]config.Model{"shared": model}, Providers: map[string]config.Provider{
				"a": {API: ai.APIChatCompletions, BaseURL: "http://fixture.invalid", APIKey: "key"},
				"b": {API: ai.APIChatCompletions, BaseURL: "http://fixture.invalid", APIKey: "key"},
			}}
			route, err := ResolveFailoverRouteContext(t.Context(), cfg, "shared", "a", false)
			if err != nil {
				t.Fatal(err)
			}
			if got := len(route.Client.(*FailoverClient).candidates); got != tc.want {
				t.Fatalf("candidates=%d want %d", got, tc.want)
			}
		})
	}
}
func TestCandidateRequestKeepsLowerExplicitOutput(t *testing.T) {
	c := failoverCandidate{maxOutput: 32000}
	for _, input := range []int{0, 128000, 12000} {
		want := 32000
		if input == 12000 {
			want = 12000
		}
		if got := c.request(ai.Request{MaxTokens: input}).MaxTokens; got != want {
			t.Fatalf("%d -> %d want %d", input, got, want)
		}
	}
}
