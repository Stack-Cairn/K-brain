package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func TestFailedStreamDoesNotExecuteToolsAndRetainsUsage(t *testing.T) {
	for protocol, events := range map[string][]string{
		"chat": {
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call","function":{"name":"write","arguments":"{}"}}]}}],"usage":{"prompt_tokens":17,"completion_tokens":3}}`,
		},
		"responses": {
			`{"type":"response.output_item.added","item":{"type":"function_call","id":"item","call_id":"call","name":"write","arguments":"{}"}}`,
			`{"type":"response.created","response":{"usage":{"input_tokens":17,"output_tokens":3}}}`,
		},
		"anthropic": {
			`{"type":"message_start","message":{"usage":{"input_tokens":17,"output_tokens":3}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call","name":"write","input":{}}}`,
			`{"type":"content_block_stop","index":0}`,
		},
	} {
		for _, failure := range []string{"disconnect", "provider error"} {
			t.Run(protocol+"/"+failure, func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					for _, event := range events {
						fmt.Fprintf(w, "data: %s\n\n", event)
					}
					if failure == "provider error" {
						fmt.Fprint(w, "data: {\"type\":\"error\",\"error\":{\"message\":\"upstream failed\"}}\n\n")
					}
				}))
				defer srv.Close()
				var client ai.Client
				switch protocol {
				case "responses":
					client = ai.NewResponses(srv.URL, "key")
				case "anthropic":
					client = ai.NewAnthropic(srv.URL, "key")
				default:
					client = ai.New(srv.URL, "key")
				}
				ag := New(client, "model", 100, "system")
				var runs atomic.Int32
				ag.Tools = []tools.Tool{{Def: ai.NewTool("write", "write", `{"type":"object"}`), Run: func(context.Context, json.RawMessage) (string, error) { runs.Add(1); return "executed", nil }}}
				_, err := ag.Turn(t.Context(), "go", Events{})
				if err == nil || runs.Load() != 0 {
					t.Fatalf("runs=%d err=%v", runs.Load(), err)
				}
				u := ag.UsageSummary().Total
				if u.PromptTokens != 17 || u.CompletionTokens != 3 {
					t.Fatalf("lost failed request usage: %+v", u)
				}
				for _, msg := range ag.Messages {
					if msg.Role == "tool" || len(msg.ToolCalls) != 0 {
						t.Fatalf("failed response entered history: %+v", msg)
					}
				}
			})
		}
	}
}
