package ai

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUsageAddPreservesMixedCacheCounters(t *testing.T) {
	var chat Usage
	if err := json.Unmarshal([]byte(`{"prompt_tokens":100,"completion_tokens":10,"prompt_tokens_details":{"cached_tokens":60},"prompt_cache_hit_tokens":60}`), &chat); err != nil {
		t.Fatal(err)
	}
	responses := Usage{PromptTokens: 200, CompletionTokens: 20, PromptCacheHitTokens: 120, PromptCacheWriteTokens: 30}
	for _, parts := range [][]Usage{{chat, responses}, {responses, chat}} {
		var sum Usage
		for _, u := range parts {
			sum.Add(u)
		}
		if sum.PromptTokens != 300 || sum.CompletionTokens != 30 || sum.Cached() != 180 || sum.CacheWrite() != 30 {
			t.Fatalf("mixed usage = %+v", sum)
		}
		if got, ok := CalculateCost(sum, TokenRates{Input: 2, Output: 3, CacheRead: 0.5, CacheWrite: 2}); !ok || math.Abs(got.Total-420) > 1e-9 {
			t.Fatalf("mixed cost = %+v, %v", got, ok)
		}
		sum.PromptTokensDetails.CachedTokens = 0
		if chat.Cached() != 60 {
			t.Fatal("addition modified source")
		}
	}
}

func TestAnthropicUsageIncludesCacheAndMergesDelta(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !stream {
					fmt.Fprint(w, `{"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":80,"cache_creation_input_tokens":20}}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":1,\"cache_read_input_tokens\":70,\"cache_creation_input_tokens\":20}}}\n\n")
				fmt.Fprint(w, "data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":3,\"cache_read_input_tokens\":80}}\n\n")
				fmt.Fprint(w, "data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":5}}\n\n")
				fmt.Fprint(w, "data: {\"type\":\"message_stop\"}\n\n")
			}))
			defer srv.Close()
			client := NewAnthropic(srv.URL, "key")
			var u Usage
			var err error
			if stream {
				_, u, err = client.Stream(t.Context(), Request{Model: "model"}, nil, nil, nil)
			} else {
				_, u, err = client.Complete(t.Context(), Request{Model: "model"})
			}
			if err != nil {
				t.Fatal(err)
			}
			if u.PromptTokens != 110 || u.CompletionTokens != 5 || u.Cached() != 80 || u.CacheWrite() != 20 {
				t.Fatalf("anthropic usage = %+v", u)
			}
			if cost, ok := CalculateCost(u, TokenRates{Input: 2, Output: 3, CacheRead: 0.5, CacheWrite: 4}); !ok || cost.Total != 155 {
				t.Fatalf("anthropic cost = %+v, %v", cost, ok)
			}
		})
	}
}
