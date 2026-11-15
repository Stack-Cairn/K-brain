package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
)

func TestDeepSeekChatSearchWireAndReplay(t *testing.T) {
	for _, effort := range []string{"off", "high"} {
		t.Run(effort, func(t *testing.T) {
			var payload map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/chat/completions" {
					t.Errorf("path: %s", r.URL.Path)
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				for _, data := range []string{
					`{"choices":[{"delta":{"reasoning_content":"Earlier thinking"}}]}`,
					`{"choices":[{"delta":{},"server_tool_event":{"type":"step_reasoning_done","step":0}}]}`,
					`{"choices":[{"delta":{},"server_tool_event":{"type":"call_started","step":0,"call":{"index":0,"id":"search-1","type":"deepseek_web_search","action":{"type":"search","queries":["latest"]}}}}]}`,
					`{"choices":[{"delta":{},"server_tool_event":{"type":"call_completed","step":0,"call":{"index":0,"id":"search-1","type":"deepseek_web_search","status":"completed","sources":[{"url":"https://example.test/news","title":"News"},{"url":"file:///secret"}]}}}]}`,
					`{"choices":[{"delta":{"reasoning_content":"Checking","content":"Answer","server_tool_calls":[{"index":0,"reasoning_content":"Checking","calls":[{"index":0,"id":"search-1","type":"deepseek_web_search","action":{"type":"search","queries":["latest"]},"status":"completed","sources":[{"url":"https://example.test/news","title":"News"}]},{"index":1,"id":"read-1","type":"deepseek_read_url","status":"completed","page":{"url":"https://example.test/page","title":"Page"},"signature":"opaque-signed-page"}]}],"annotations":[{"type":"url_citation","url_citation":{"url":"https://example.test/news","title":"News","start_index":0,"end_index":6}}]},"finish_reason":"stop"}]}`,
					`[DONE]`,
				} {
					fmt.Fprintf(w, "data: %s\n\n", data)
				}
			}))
			defer srv.Close()
			var events []HostedSearch
			ctx := WithSearchObserver(context.Background(), func(b HostedSearch) { events = append(events, b) })
			c := New(srv.URL, "key")
			req := Request{Model: "deepseek-flash", NativeWebSearch: true, ReasoningEffort: effort, Messages: []Message{{Role: "user", Content: "latest?"}}}
			if effort == "high" {
				req.Tools = []Tool{NewTool("read_file", "Read", `{"type":"object"}`)}
			}
			msg, _, err := c.Stream(ctx, req, nil, nil, func(_, _, _ string) { t.Error("server tools must not be executed by client") })
			if err != nil {
				t.Fatal(err)
			}
			tools := payload["tools"].([]any)
			offset := 0
			if effort == "high" {
				offset = 1
				if tools[0].(map[string]any)["type"] != "function" {
					t.Fatal(tools)
				}
			}
			if len(tools) != offset+2 || tools[offset].(map[string]any)["type"] != "deepseek_web_search" || tools[offset+1].(map[string]any)["type"] != "deepseek_read_url" {
				t.Fatalf("tools: %#v", tools)
			}
			if payload["web_search_options"] != nil {
				t.Fatal("unsupported web_search_options")
			}
			kind := "enabled"
			if effort == "off" {
				kind = "disabled"
			}
			if payload["thinking"].(map[string]any)["type"] != kind {
				t.Fatalf("thinking: %#v", payload)
			}
			if msg.Content != "Answer" || len(msg.HostedSearch) != 2 || len(msg.ToolCalls) != 0 || msg.Replay == nil {
				t.Fatalf("message: %#v", msg)
			}
			if len(events) < 3 || events[0].Status != "searching" || msg.HostedSearch[0].Status != "completed" || len(msg.HostedSearch[0].Sources) != 1 || msg.HostedSearch[0].Sources[0].SourceType != "citation" {
				t.Fatalf("search: %#v / %#v", events, msg.HostedSearch)
			}
			// Round-trip persisted history exactly as session restore does.
			encoded, err := json.Marshal(msg)
			if err != nil {
				t.Fatal(err)
			}
			var restored Message
			if err := json.Unmarshal(encoded, &restored); err != nil {
				t.Fatal(err)
			}
			for _, route := range []struct {
				model, endpoint string
				replay          bool
			}{
				{"deepseek-flash", srv.URL, true}, {"deepseek-chat", srv.URL, false}, {"deepseek-flash", srv.URL + "/other", false},
			} {
				client := New(route.endpoint, "key")
				body, err := client.chatBody(Request{Model: route.model, Messages: prepareRequestHistory([]Message{restored}, APIChatCompletions, route.endpoint, route.model)})
				if err != nil {
					t.Fatal(err)
				}
				var p map[string]any
				_ = json.Unmarshal(body, &p)
				row := p["messages"].([]any)[0].(map[string]any)
				if (row["server_tool_calls"] != nil) != route.replay {
					t.Fatalf("route replay: %#v", row)
				}
				if route.replay {
					calls := row["server_tool_calls"].([]any)[0].(map[string]any)["calls"].([]any)
					if calls[1].(map[string]any)["signature"] != "opaque-signed-page" || row["reasoning_content"] != "Checking" {
						t.Fatalf("lost replay: %#v", row)
					}
				}
			}
		})
	}
}

func TestDeepSeekChatSearchGatesAndDisabled(t *testing.T) {
	c := New("https://relay.test", "key")
	for _, model := range []string{"gpt-4o", "deepseek-chat", "deepseek-reasoner", "deepseek-pro", "deepseek-v4-flash"} {
		if _, err := c.chatBody(Request{Model: model, NativeWebSearch: true}); err == nil {
			t.Fatalf("search unexpectedly supported: %s", model)
		}
	}
	for _, model := range []string{"deepseek-flash", "gpt-4o"} {
		body, err := c.chatBody(Request{Model: model, Messages: []Message{{Role: "user", Content: "hello"}}})
		if err != nil {
			t.Fatal(err)
		}
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		if payload["tools"] != nil {
			t.Fatalf("search disabled but tools sent: %s", body)
		}
	}
}

func TestDeepSeekSearchFailureAndInterruptedStream(t *testing.T) {
	for _, complete := range []bool{true, false} {
		t.Run(fmt.Sprint(complete), func(t *testing.T) {
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"server_tool_event\":{\"type\":\"call_started\",\"call\":{\"id\":\"s\",\"type\":\"deepseek_web_search\",\"action\":{\"queries\":[\"q\"]}}}}]}\n\n")
				if complete {
					fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"server_tool_event\":{\"type\":\"call_completed\",\"call\":{\"id\":\"s\",\"type\":\"deepseek_web_search\",\"status\":\"failed\",\"error\":{\"code\":\"search_failed\"}}},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
				}
			}))
			defer srv.Close()
			var states []string
			var problem string
			c := New(srv.URL, "key")
			c.MaxRetries = 2
			ctx := WithSearchObserver(context.Background(), func(b HostedSearch) { states = append(states, b.Status); problem = b.Error })
			_, _, err := c.Stream(ctx, Request{Model: "deepseek-flash", NativeWebSearch: true, Messages: []Message{{Role: "user", Content: "search"}}}, nil, nil, nil)
			if (err == nil) != complete {
				t.Fatalf("error: %v", err)
			}
			if !reflect.DeepEqual(states, []string{"searching", "failed"}) {
				t.Fatalf("states: %v", states)
			}
			if requests.Load() != 1 {
				t.Fatalf("visible search was retried: %d", requests.Load())
			}
			if complete && problem != "search_failed" {
				t.Fatalf("problem: %s", problem)
			}
		})
	}
}
