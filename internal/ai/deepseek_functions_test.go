package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDeepSeekMixedDisabledUsesIndependentAPIs(t *testing.T) {
	for _, effort := range []string{"off", "none", ""} {
		for _, parallel := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/parallel=%v", effort, parallel), func(t *testing.T) {
				chats, searches, fetches := 0, 0, 0
				var lastPayload map[string]any
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Authorization") != "Bearer key" || r.Header.Get("X-Relay") != "yes" {
						t.Error("lost credentials or headers")
					}
					var p map[string]any
					if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
						t.Error(err)
					}
					switch r.URL.Path {
					case "/v1/tools/search":
						searches++
						if p["query"] != "latest" {
							t.Error(p)
						}
						fmt.Fprint(w, `{"results":[{"url":"https://example.test/news","title":"News","summary":"Verified news"}]}`)
					case "/v1/tools/fetch":
						fetches++
						if p["url"] != "https://example.test/news" {
							t.Error(p)
						}
						fmt.Fprint(w, `{"url":"https://example.test/news","title":"News","content":"Full verified page"}`)
					case "/v1/chat/completions":
						chats++
						lastPayload = p
						for _, raw := range p["tools"].([]any) {
							if raw.(map[string]any)["type"] != "function" {
								http.Error(w, "client tools can only be used together with server tools in thinking mode", 400)
								t.Error("invalid mixed tools")
								return
							}
						}
						if effort == "" {
							if p["thinking"] != nil {
								t.Error("changed unspecified thinking", p)
							}
						} else if p["thinking"].(map[string]any)["type"] != "disabled" {
							t.Error("enabled thinking", p)
						}
						var calls []map[string]any
						if chats == 1 {
							calls = append(calls, map[string]any{"index": 0, "id": "s", "type": "function", "function": map[string]string{"name": deepSeekSearchFunction, "arguments": `{"query":"latest"}`}})
							if parallel {
								calls = append(calls, map[string]any{"index": 1, "id": "local", "type": "function", "function": map[string]string{"name": "read_file", "arguments": `{"path":"package.json"}`}})
							}
						} else if chats == 2 && !parallel {
							calls = append(calls, map[string]any{"index": 0, "id": "f", "type": "function", "function": map[string]string{"name": deepSeekFetchFunction, "arguments": `{"url":"https://example.test/news"}`}})
						} else {
							calls = append(calls, map[string]any{"index": 0, "id": "local", "type": "function", "function": map[string]string{"name": "read_file", "arguments": `{"path":"package.json"}`}})
						}
						data, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": calls}, "finish_reason": "tool_calls"}}, "usage": map[string]int{"prompt_tokens": 2, "completion_tokens": 1}})
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", data)
					default:
						t.Error(r.URL.Path)
						http.NotFound(w, r)
					}
				}))
				defer srv.Close()
				c := NewWithOptions(srv.URL+"/v1/chat/completions", "key", true, "", map[string]string{"X-Relay": "yes"})
				var events []HostedSearch
				ctx := WithSearchObserver(context.Background(), func(b HostedSearch) { events = append(events, b) })
				var callbacks []string
				req := Request{Model: "deepseek-flash", NativeWebSearch: true, ReasoningEffort: effort, Messages: []Message{{Role: "user", Content: "Search then read package.json"}}, Tools: []Tool{NewTool("read_file", "Read", `{"type":"object"}`)}}
				msg, usage, err := c.Stream(ctx, req, nil, nil, func(_, name, _ string) { callbacks = append(callbacks, name) })
				if err != nil {
					t.Fatal(err)
				}
				wantChats, wantFetch := 3, 1
				if parallel {
					wantChats, wantFetch = 1, 0
				}
				if chats != wantChats || searches != 1 || fetches != wantFetch || usage.PromptTokens != 2*wantChats {
					t.Fatalf("requests %d/%d/%d usage %+v", chats, searches, fetches, usage)
				}
				if len(callbacks) != 1 || callbacks[0] != "read_file" || len(msg.ToolCalls) != 1 || msg.ToolCalls[0].Function.Name != "read_file" {
					t.Fatalf("local tool dispatch: %+v %v", msg, callbacks)
				}
				if len(events) != 2*(1+wantFetch) || len(msg.HostedSearch) != 1+wantFetch || msg.HostedSearch[0].Status != "completed" || len(msg.HostedSearch[0].Sources) != 1 {
					t.Fatalf("search progress %+v / %+v", events, msg)
				}
				// Replay survives session persistence and contains completed private tool turns
				// before the public local-tool call, but cannot leak across routes/models.
				data, _ := json.Marshal(msg)
				var restored Message
				if err := json.Unmarshal(data, &restored); err != nil {
					t.Fatal(err)
				}
				for _, same := range []bool{true, false} {
					model := "deepseek-flash"
					if !same {
						model = "deepseek-chat"
					}
					body, err := c.chatBody(Request{Model: model, Messages: prepareRequestHistory([]Message{restored}, APIChatCompletions, c.Endpoint(), model)})
					if err != nil {
						t.Fatal(err)
					}
					if strings.Contains(string(body), "Verified news") != same {
						t.Fatalf("private replay scope: %s", body)
					}
				}
				if !parallel && !strings.Contains(fmt.Sprint(lastPayload["messages"]), "Full verified page") {
					t.Fatal("fetch result not given to model")
				}
			})
		}
	}
}

func TestDeepSeekIndependentAPIFailures(t *testing.T) {
	for _, mode := range []string{"auth", "malformed", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			release := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "auth":
					http.Error(w, "bad key", 401)
				case "malformed":
					fmt.Fprint(w, "not json")
				case "cancel":
					// Drain the POST body so net/http can observe the peer closing.
					_, _ = io.Copy(io.Discard, r.Body)
					select {
					case <-r.Context().Done():
					case <-release:
					}
				}
			}))
			defer srv.Close()
			defer close(release)
			c := New(srv.URL, "key")
			c.MaxRetries = 0
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			var states []string
			ctx = WithSearchObserver(ctx, func(b HostedSearch) { states = append(states, b.Status) })
			call := ToolCall{ID: "s"}
			call.Function.Name = deepSeekSearchFunction
			call.Function.Arguments = `{"query":"latest"}`
			_, err := c.deepSeekFunctionResult(ctx, call, newSearchStream(ctx, "deepseek"))
			if err == nil || strings.Join(states, ",") != "searching,failed" {
				t.Fatalf("%v %v", err, states)
			}
		})
	}
}
