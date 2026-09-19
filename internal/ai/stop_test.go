package ai

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestOutputLimitRetainsPartialResponse(t *testing.T) {
	for _, tc := range []struct {
		protocol, raw, stream, complete string
		client                          func(string) Client
	}{
		{"chat", "length", streamFrames(
			`{"choices":[{"delta":{"content":"partial"}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call","function":{"name":"write","arguments":"{}"}}]},"finish_reason":"length"}],"usage":{"prompt_tokens":37,"completion_tokens":9,"prompt_cache_hit_tokens":17,"prompt_cache_write_tokens":5}}`,
			`[DONE]`,
		), `{"choices":[{"message":{"content":"partial"},"finish_reason":"length"}],"usage":{"prompt_tokens":37,"completion_tokens":9,"prompt_cache_hit_tokens":17,"prompt_cache_write_tokens":5}}`, func(url string) Client { return New(url, "key") }},
		{"responses", "incomplete.max_output_tokens", streamFrames(
			`{"type":"response.output_text.delta","delta":"par"}`,
			`{"type":"response.output_item.added","item":{"id":"item","type":"function_call","call_id":"call","name":"write","arguments":"{}"}}`,
			`{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message","content":[{"type":"output_text","text":"partial"}]},{"type":"function_call","arguments":"{\"path\":"}],"usage":{"input_tokens":37,"output_tokens":9,"input_tokens_details":{"cached_tokens":17,"cache_write_tokens":5}}}}`,
		), `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message","content":[{"type":"output_text","text":"partial"}]}],"usage":{"input_tokens":37,"output_tokens":9,"input_tokens_details":{"cached_tokens":17,"cache_write_tokens":5}}}`, func(url string) Client { return NewResponses(url, "key") }},
		{"anthropic", "max_tokens", streamFrames(
			`{"type":"message_start","message":{"usage":{"input_tokens":15,"cache_read_input_tokens":17,"cache_creation_input_tokens":5}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"partial"}}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call","name":"write","input":{}}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{}"}}`,
			`{"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":9}}`,
			`{"type":"message_stop"}`,
		), `{"content":[{"type":"text","text":"partial"}],"stop_reason":"max_tokens","usage":{"input_tokens":15,"output_tokens":9,"cache_read_input_tokens":17,"cache_creation_input_tokens":5}}`, func(url string) Client { return NewAnthropic(url, "key") }},
	} {
		t.Run(tc.protocol, func(t *testing.T) {
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				if strings.Contains(string(body), `"stop_reason"`) || strings.Contains(string(body), `"raw_stop_reason"`) {
					t.Error("local stop metadata sent to provider")
				}
				var request struct {
					Stream bool `json:"stream"`
				}
				if err := json.Unmarshal(body, &request); err != nil {
					t.Error(err)
				}
				if request.Stream {
					fmt.Fprint(w, tc.stream)
				} else {
					fmt.Fprint(w, tc.complete)
				}
			}))
			defer srv.Close()
			client := tc.client(srv.URL)
			prior := Message{Role: "assistant", Content: "earlier", StopReason: StopReasonLength, RawStopReason: tc.raw}
			req := Request{Model: "model", Messages: []Message{{Role: "user", Content: "hello"}, prior}}
			var text strings.Builder
			msg, usage, err := client.Stream(t.Context(), req, func(s string) { text.WriteString(s) }, nil, nil)
			if err != nil || msg.StopReason != StopReasonLength || msg.RawStopReason != tc.raw || len(msg.ToolCalls) != 0 {
				t.Fatalf("partial message: %+v, %v", msg, err)
			}
			if text.String() != msg.Content || !strings.HasPrefix(msg.Content, "partial\n") || strings.Count(msg.Content, "tool calls discarded") != 1 {
				t.Fatalf("stream/final text differ: %q, %q", text.String(), msg.Content)
			}
			if usage.PromptTokens != 37 || usage.CompletionTokens != 9 || usage.Cached() != 17 || usage.CacheWrite() != 5 {
				t.Fatalf("partial usage: %+v", usage)
			}
			if req.Messages[1].StopReason != StopReasonLength {
				t.Fatal("request mutated original metadata")
			}
			data, err := json.Marshal(msg)
			if err != nil {
				t.Fatal(err)
			}
			var restored Message
			if err := json.Unmarshal(data, &restored); err != nil {
				t.Fatal(err)
			}
			if restored.StopReason != msg.StopReason || restored.RawStopReason != msg.RawStopReason || restored.Content != msg.Content {
				t.Fatalf("stop metadata lost in round trip: %+v", restored)
			}
			if err := json.Unmarshal([]byte(`{"role":"user","content":"fresh"}`), &restored); err != nil {
				t.Fatal(err)
			}
			if restored.StopReason != "" || restored.RawStopReason != "" {
				t.Fatal("stale stop metadata")
			}
			partial, u, err := client.Complete(t.Context(), req)
			var limit *OutputLimitError
			if !errors.As(err, &limit) || limit.Reason != tc.raw || retryable(err) || partial != "partial" || u.PromptTokens != 37 || u.CacheWrite() != 5 {
				t.Fatalf("complete: %q, %+v, %v", partial, u, err)
			}
			if requests.Load() != 2 {
				t.Fatalf("retried accepted output: %d", requests.Load())
			}
		})
	}
}

func TestResponsesIncompleteTerminalValidation(t *testing.T) {
	for _, tc := range []struct {
		name, event, status, reason, providerError string
		wantError                                  bool
	}{
		{"limit", "incomplete", "incomplete", "max_output_tokens", "null", false},
		{"missing status", "incomplete", "", "max_output_tokens", "null", false},
		{"missing reason", "incomplete", "incomplete", "", "null", true},
		{"filter", "incomplete", "incomplete", "content_filter", "null", true},
		{"unknown reason", "incomplete", "incomplete", "other", "null", true},
		{"provider error", "incomplete", "incomplete", "max_output_tokens", `{"message":"failed upstream"}`, true},
		{"conflicting event", "completed", "incomplete", "max_output_tokens", "null", true},
		{"conflicting status", "incomplete", "completed", "max_output_tokens", "null", true},
		{"still queued", "completed", "queued", "", "null", true},
		{"still running", "completed", "in_progress", "", "null", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := streamFrames(fmt.Sprintf(`{"type":"response.%s","response":{"status":%q,"incomplete_details":{"reason":%q},"error":%s,"usage":{"input_tokens":3,"output_tokens":2}}}`, tc.event, tc.status, tc.reason, tc.providerError))
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
			defer srv.Close()
			var text strings.Builder
			msg, u, err := NewResponses(srv.URL, "key").Stream(t.Context(), Request{}, func(s string) { text.WriteString(s) }, nil, nil)
			if (err != nil) != tc.wantError || u.PromptTokens != 3 || u.CompletionTokens != 2 {
				t.Fatalf("result=%+v usage=%+v error=%v", msg, u, err)
			}
			if !tc.wantError && (msg.StopReason != StopReasonLength || text.String() != msg.Content || !strings.Contains(msg.Content, "truncated")) {
				t.Fatalf("empty truncation not reported: %+v", msg)
			}
			if tc.wantError && (msg.Content != "" || len(msg.ToolCalls) > 0) {
				t.Fatalf("failed response returned as success: %+v", msg)
			}
		})
	}
}
