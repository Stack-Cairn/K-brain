package ai

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// streamFixture serves the same SSE body to every request.
func streamFixture(t *testing.T, events ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, streamFrames(events...))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestStreamedReasoningIsKeptOnTheMessage(t *testing.T) {
	cases := []struct {
		name   string
		client func(url string) Client
		events []string
		want   string
		text   string
	}{{
		name:   "chat completions",
		client: func(url string) Client { return New(url, "key") },
		events: []string{
			`{"choices":[{"delta":{"reasoning_content":"think","role":"assistant"}}]}`,
			`{"choices":[{"delta":{"reasoning_content":"ing"}}]}`,
			`{"choices":[{"delta":{"content":"4"}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
			`[DONE]`,
		},
		want: "thinking",
		text: "4",
	}, {
		name:   "responses summary parts",
		client: func(url string) Client { return NewResponses(url, "key") },
		events: []string{
			`{"type":"response.reasoning_summary_text.delta","item_id":"rs","summary_index":0,"delta":"**Plan**"}`,
			`{"type":"response.reasoning_summary_text.delta","item_id":"rs","summary_index":0,"delta":" add them"}`,
			`{"type":"response.reasoning_summary_text.delta","item_id":"rs","summary_index":1,"delta":"**Check**"}`,
			`{"type":"response.output_item.done","item":{"type":"reasoning","id":"rs","summary":[{"type":"summary_text","text":"**Plan** add them"},{"type":"summary_text","text":"**Check**"}],"encrypted_content":"sealed"}}`,
			`{"type":"response.output_text.delta","delta":"4"}`,
			`{"type":"response.completed","response":{"status":"completed","output":[{"type":"reasoning","id":"rs","summary":[{"type":"summary_text","text":"**Plan** add them"},{"type":"summary_text","text":"**Check**"}]},{"type":"message","content":[{"type":"output_text","text":"4"}]}]}}`,
		},
		want: "**Plan** add them\n\n**Check**",
		text: "4",
	}, {
		name:   "responses reasoning text",
		client: func(url string) Client { return NewResponses(url, "key") },
		events: []string{
			`{"type":"response.reasoning_text.delta","item_id":"rs","content_index":0,"delta":"two plus two"}`,
			`{"type":"response.output_text.delta","delta":"4"}`,
			`{"type":"response.completed","response":{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"4"}]}]}}`,
		},
		want: "two plus two",
		text: "4",
	}, {
		name:   "anthropic messages",
		client: func(url string) Client { return NewAnthropic(url, "key") },
		events: []string{
			`{"type":"message_start","message":{"usage":{"input_tokens":3}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"add "}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"them"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"4"}}`,
			`{"type":"content_block_stop","index":1}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
			`{"type":"message_stop"}`,
		},
		want: "add them",
		text: "4",
	}, {
		name:   "gemini",
		client: func(url string) Client { return NewGemini(url, "key") },
		events: []string{
			`{"candidates":[{"content":{"role":"model","parts":[{"text":"add ","thought":true}]}}]}`,
			`{"candidates":[{"content":{"role":"model","parts":[{"text":"them","thought":true}]}}]}`,
			`{"candidates":[{"content":{"role":"model","parts":[{"text":"4"}]},"finishReason":"STOP"}]}`,
		},
		want: "add them",
		text: "4",
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := streamFixture(t, tc.events...)
			var live strings.Builder
			msg, _, err := tc.client(srv.URL).Stream(t.Context(), Request{Model: "model"}, nil, func(d string) { live.WriteString(d) }, nil)
			if err != nil {
				t.Fatal(err)
			}
			if msg.Reasoning != tc.want {
				t.Fatalf("stored reasoning = %q, want %q", msg.Reasoning, tc.want)
			}
			if live.String() != tc.want {
				t.Fatalf("live reasoning = %q, stored %q", live.String(), msg.Reasoning)
			}
			if msg.Content != tc.text {
				t.Fatalf("content = %q", msg.Content)
			}
		})
	}
}

// A Responses stream that sends no reasoning deltas still yields the summary of
// its finished reasoning item, and reasoning content never leaks into the reply.
func TestResponsesReasoningFromFinishedItem(t *testing.T) {
	srv := streamFixture(t,
		`{"type":"response.output_item.done","item":{"type":"reasoning","id":"rs","summary":[{"type":"summary_text","text":"weighed the options"}],"encrypted_content":"sealed"}}`,
		`{"type":"response.completed","response":{"status":"completed","output":[{"type":"reasoning","id":"rs","summary":[{"type":"summary_text","text":"weighed the options"}],"content":[{"type":"reasoning_text","text":"raw chain"}]},{"type":"message","content":[{"type":"output_text","text":"done"}]}]}}`,
	)
	var live strings.Builder
	msg, _, err := NewResponses(srv.URL, "key").Stream(t.Context(), Request{Model: "model"}, nil, func(d string) { live.WriteString(d) }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Reasoning != "weighed the options" {
		t.Fatalf("reasoning = %q", msg.Reasoning)
	}
	if msg.Content != "done" {
		t.Fatalf("content = %q", msg.Content)
	}
	if live.Len() != 0 {
		t.Fatalf("finished item replayed as live thinking: %q", live.String())
	}

	srv = streamFixture(t,
		`{"type":"response.completed","response":{"status":"completed","output":[{"type":"reasoning","id":"rs","summary":[],"content":[{"type":"reasoning_text","text":"raw chain"}]},{"type":"message","content":[{"type":"output_text","text":"done"}]}]}}`,
	)
	msg, _, err = NewResponses(srv.URL, "key").Stream(t.Context(), Request{Model: "model"}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Reasoning != "raw chain" || msg.Content != "done" {
		t.Fatalf("message = %+v", msg)
	}
}

// Kept reasoning is display-only: the next request of every wire format is
// byte-identical with and without it.
func TestStoredReasoningDoesNotChangeRequests(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(body))
		mu.Unlock()
		http.Error(w, `{"error":{"message":"fixture stop"}}`, http.StatusBadRequest)
	}))
	defer srv.Close()
	clients := map[string]func() Client{
		"chat":      func() Client { return New(srv.URL, "key") },
		"responses": func() Client { return NewResponses(srv.URL, "key") },
		"anthropic": func() Client { return NewAnthropic(srv.URL, "key") },
		"gemini":    func() Client { return NewGemini(srv.URL, "key") },
	}
	history := func(reasoning string) []Message {
		return []Message{
			{Role: "user", Content: "add 2 and 2"},
			{Role: "assistant", Content: "4", Reasoning: reasoning, StopReason: StopReasonStop},
			{Role: "user", Content: "and 3 more?"},
		}
	}
	for name, client := range clients {
		t.Run(name, func(t *testing.T) {
			mu.Lock()
			bodies = nil
			mu.Unlock()
			for _, reasoning := range []string{"", "a private chain of thought"} {
				if _, _, err := client().Stream(t.Context(), Request{Model: "model", Messages: history(reasoning)}, nil, nil, nil); err == nil {
					t.Fatal("fixture error was not returned")
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if len(bodies) != 2 {
				t.Fatalf("requests = %d", len(bodies))
			}
			if bodies[0] != bodies[1] || strings.Contains(bodies[1], "private chain") {
				t.Fatalf("display reasoning changed the request:\n%s\n%s", bodies[0], bodies[1])
			}
		})
	}
}

// DeepSeek function search runs several chat rounds for one reply; the reply
// keeps the reasoning of every round, as the user saw it stream.
func TestDeepSeekFunctionSearchKeepsEveryRoundsReasoning(t *testing.T) {
	chats := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/tools/search":
			fmt.Fprint(w, `{"results":[{"url":"https://example.test/news","title":"News","summary":"Verified news"}]}`)
		case "/v1/chat/completions":
			chats++
			w.Header().Set("Content-Type", "text/event-stream")
			if chats == 1 {
				fmt.Fprint(w, streamFrames(
					`{"choices":[{"delta":{"reasoning_content":"look it up"}}]}`,
					fmt.Sprintf(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"s","type":"function","function":{"name":%q,"arguments":"{\"query\":\"latest\"}"}}]},"finish_reason":"tool_calls"}]}`, deepSeekSearchFunction),
					`[DONE]`))
				return
			}
			fmt.Fprint(w, streamFrames(
				`{"choices":[{"delta":{"reasoning_content":"found it"}}]}`,
				`{"choices":[{"delta":{"content":"answer"},"finish_reason":"stop"}]}`,
				`[DONE]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := NewWithOptions(srv.URL+"/v1/chat/completions", "key", true, "", nil)
	req := Request{Model: "deepseek-flash", NativeWebSearch: true, Messages: []Message{{Role: "user", Content: "news?"}}, Tools: []Tool{NewTool("read_file", "Read", `{"type":"object"}`)}}
	msg, _, err := c.Stream(t.Context(), req, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if chats != 2 || msg.Content != "answer" || msg.Reasoning != "look it up\n\nfound it" {
		t.Fatalf("chats=%d message=%+v", chats, msg)
	}
}
