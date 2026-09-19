package ai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func streamClient(protocol, url string) Client {
	switch protocol {
	case "responses":
		return NewResponses(url, "key")
	case "anthropic":
		return NewAnthropic(url, "key")
	default:
		c := New(url, "key")
		c.MaxRetries = 1
		return c
	}
}

func streamFrames(events ...string) string {
	var b strings.Builder
	for _, event := range events {
		b.WriteString("data: " + event + "\n\n")
	}
	return b.String()
}

func TestStreamsRejectInvalidResponses(t *testing.T) {
	for _, protocol := range []string{"chat", "responses", "anthropic"} {
		for name, body := range map[string]string{
			"empty":       "",
			"html":        "<html>upstream unavailable</html>",
			"malformed":   streamFrames("{broken"),
			"null":        streamFrames("null"),
			"error":       streamFrames(`{"error":{"type":"overloaded","message":"try later"}}`),
			"event error": "event: error\ndata: {\"message\":\"try later\"}\n\n",
		} {
			t.Run(protocol+"/"+name, func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, body) }))
				defer srv.Close()
				msg, _, err := streamClient(protocol, srv.URL).Stream(t.Context(), Request{Model: "model"}, nil, nil, nil)
				if err == nil || len(msg.ToolCalls) != 0 {
					t.Fatalf("accepted invalid stream: %+v, %v", msg, err)
				}
				if strings.Contains(name, "error") && !strings.Contains(err.Error(), "try later") {
					t.Fatalf("lost provider error: %v", err)
				}
			})
		}
	}
}

func TestStreamFailurePreservesUsageAndDiscardsTools(t *testing.T) {
	for _, protocol := range []string{"chat", "responses", "anthropic"} {
		var prefix string
		switch protocol {
		case "chat":
			prefix = streamFrames(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call","function":{"name":"write","arguments":"{}"}}]}}],"usage":{"prompt_tokens":17,"completion_tokens":3}}`)
		case "responses":
			prefix = streamFrames(`{"type":"response.created","response":{"usage":{"input_tokens":17,"output_tokens":3}}}`, `{"type":"response.output_item.added","item":{"type":"function_call","id":"item","call_id":"call","name":"write","arguments":"{}"}}`)
		case "anthropic":
			prefix = streamFrames(`{"type":"message_start","message":{"usage":{"input_tokens":17,"output_tokens":3}}}`, `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call","name":"write","input":{}}}`, `{"type":"content_block_stop","index":0}`)
		}
		for _, ending := range []string{"eof", "error"} {
			t.Run(protocol+"/"+ending, func(t *testing.T) {
				body := prefix
				if ending == "error" {
					body += streamFrames(`{"type":"error","error":{"message":"provider failed"}}`)
				}
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, body) }))
				defer srv.Close()
				msg, usage, err := streamClient(protocol, srv.URL).Stream(t.Context(), Request{Model: "model"}, nil, nil, nil)
				if err == nil || len(msg.ToolCalls) != 0 || usage.PromptTokens != 17 || usage.CompletionTokens != 3 {
					t.Fatalf("msg=%+v usage=%+v err=%v", msg, usage, err)
				}
				if ending == "eof" && !errors.Is(err, io.ErrUnexpectedEOF) {
					t.Fatalf("expected premature EOF: %v", err)
				}
			})
		}
	}
}

func TestResponsesInterleavedTools(t *testing.T) {
	body := streamFrames(
		`{"type":"response.output_item.added","item":{"type":"function_call","id":"item1","call_id":"call1","name":"read","arguments":""}}`,
		`{"type":"response.output_item.added","item":{"type":"function_call","id":"item2","call_id":"call2","name":"write","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","item_id":"item1","delta":"{\"path\":"}`,
		`{"type":"response.function_call_arguments.delta","item_id":"item2","delta":"{\"text\":\"hi\"}"}`,
		`{"type":"response.function_call_arguments.delta","item_id":"item1","delta":"\"a\"}"}`,
		`{"type":"response.function_call_arguments.done","item_id":"item1","arguments":"{\"path\":\"a\"}"}`,
		`{"type":"response.output_item.done","item":{"type":"function_call","id":"item2","call_id":"call2","name":"write","arguments":"{\"text\":\"hi\"}"}}`,
		`{"type":"response.completed","response":{"status":"completed","output":[{"type":"function_call","id":"item1","call_id":"call1","name":"read","arguments":"{\"path\":\"a\"}"},{"type":"function_call","id":"item2","call_id":"call2","name":"write","arguments":"{\"text\":\"hi\"}"}],"usage":{"input_tokens":12,"output_tokens":4}}}`,
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, body) }))
	defer srv.Close()
	seen := map[string]string{}
	msg, u, err := NewResponses(srv.URL, "key").Stream(t.Context(), Request{Model: "model"}, nil, nil, func(id, name, args string) {
		if (id != "call1" && id != "call2") || name == "" {
			t.Errorf("unstable callback identity: %q %q", id, name)
		}
		seen[id] = args
	})
	if err != nil || len(msg.ToolCalls) != 2 || u.PromptTokens != 12 {
		t.Fatalf("%+v %+v %v", msg, u, err)
	}
	for i, want := range []string{`{"path":"a"}`, `{"text":"hi"}`} {
		tc := msg.ToolCalls[i]
		if tc.ID != fmt.Sprintf("call%d", i+1) || tc.Function.Arguments != want || seen[tc.ID] != want {
			t.Fatalf("tool %d: %+v, callbacks=%v", i, tc, seen)
		}
	}
}

func TestResponsesTerminalFailures(t *testing.T) {
	for _, status := range []string{"failed", "incomplete", "cancelled"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", status, stream), func(t *testing.T) {
				result := fmt.Sprintf(`{"status":%q,"incomplete_details":{"reason":"content_filter"},"usage":{"input_tokens":9}}`, status)
				body := result
				if stream {
					body = streamFrames(fmt.Sprintf(`{"type":"response.%s","response":%s}`, status, result))
				}
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, body) }))
				defer srv.Close()
				c := NewResponses(srv.URL, "key")
				var u Usage
				var err error
				if stream {
					_, u, err = c.Stream(t.Context(), Request{}, nil, nil, nil)
				} else {
					_, u, err = c.Complete(t.Context(), Request{})
				}
				if err == nil || !strings.Contains(err.Error(), status) || u.PromptTokens != 9 {
					t.Fatalf("usage=%+v err=%v", u, err)
				}
			})
		}
	}
}

func TestAnthropicInterleavedToolsAndInitialInput(t *testing.T) {
	prefix := streamFrames(
		`{"type":"message_start","message":{"usage":{"input_tokens":8}}}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call1","name":"read","input":{}}}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"call2","name":"write","input":{}}}`,
		`{"type":"content_block_start","index":3,"content_block":{"type":"tool_use","id":"call3","name":"status","input":{"verbose":true}}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{}"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"a\"}"}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_stop","index":3}`,
	)
	for _, reason := range []string{"tool_use", "max_tokens"} {
		t.Run(reason, func(t *testing.T) {
			body := prefix + streamFrames(fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":%q},"usage":{"output_tokens":4}}`, reason), `{"type":"message_stop"}`)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, body) }))
			defer srv.Close()
			msg, u, err := NewAnthropic(srv.URL, "key").Stream(t.Context(), Request{}, nil, nil, nil)
			if err != nil || u.PromptTokens != 8 || u.CompletionTokens != 4 {
				t.Fatalf("%+v %v", u, err)
			}
			if reason == "max_tokens" {
				if len(msg.ToolCalls) != 0 || !strings.Contains(msg.Content, "truncated") {
					t.Fatalf("%+v", msg)
				}
				return
			}
			if len(msg.ToolCalls) != 3 {
				t.Fatalf("%+v", msg.ToolCalls)
			}
			for i, want := range []string{`{"path":"a"}`, `{}`, `{"verbose":true}`} {
				if msg.ToolCalls[i].ID != fmt.Sprintf("call%d", i+1) || msg.ToolCalls[i].Function.Arguments != want {
					t.Fatalf("tool %d: %+v", i, msg.ToolCalls[i])
				}
			}
		})
	}
}

func TestStreamsReturnAtTerminalAndRespectCancellation(t *testing.T) {
	for protocol, terminal := range map[string]string{
		"chat":      streamFrames(`{"choices":[{"delta":{"content":"ok"}}]}`, `[DONE]`),
		"responses": streamFrames(`{"type":"response.output_text.delta","delta":"ok"}`, `{"type":"response.completed","response":{"status":"completed"}}`),
		"anthropic": streamFrames(`{"type":"message_start","message":{}}`, `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"ok"}}`, `{"type":"message_stop"}`),
	} {
		for _, cancel := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/cancel=%v", protocol, cancel), func(t *testing.T) {
				release := make(chan struct{})
				ready := make(chan struct{})
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					if cancel {
						fmt.Fprint(w, ": waiting\n\n")
					} else {
						fmt.Fprint(w, terminal)
					}
					w.(http.Flusher).Flush()
					close(ready)
					select {
					case <-release:
					case <-r.Context().Done():
					}
				}))
				defer srv.Close()
				defer close(release)
				ctx, stop := context.WithTimeout(t.Context(), 2*time.Second)
				defer stop()
				if cancel {
					go func() { <-ready; stop() }()
				}
				msg, _, err := streamClient(protocol, srv.URL).Stream(ctx, Request{}, nil, nil, nil)
				if cancel {
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("expected cancellation: %v", err)
					}
					return
				}
				if err != nil || msg.Content != "ok" {
					t.Fatalf("waited past terminal: %+v %v", msg, err)
				}
			})
		}
	}
}

func TestChatNeverRetriesGeneratedContentWithoutCallbacks(t *testing.T) {
	for _, delta := range []string{
		`{"content":"hello"}`,
		`{"reasoning_content":"thinking"}`,
		`{"tool_calls":[{"index":0,"function":{"arguments":"{"}}]}`,
	} {
		t.Run(delta, func(t *testing.T) {
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				fmt.Fprint(w, streamFrames(`{"choices":[{"delta":`+delta+`}]}`))
			}))
			defer srv.Close()
			c := New(srv.URL, "key")
			c.MaxRetries = 3
			_, _, err := c.Stream(t.Context(), Request{}, nil, nil, nil)
			if !errors.Is(err, io.ErrUnexpectedEOF) || requests.Load() != 1 {
				t.Fatalf("requests=%d err=%v", requests.Load(), err)
			}
		})
	}
}

func TestCompleteHTTP200ErrorsPreserveUsage(t *testing.T) {
	for _, protocol := range []string{"chat", "responses", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			usage := `{"input_tokens":19,"output_tokens":2}`
			if protocol == "chat" {
				usage = `{"prompt_tokens":19,"completion_tokens":2}`
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprintf(w, `{"error":{"code":"service_error","message":"request failed"},"usage":%s}`, usage)
			}))
			defer srv.Close()
			text, u, err := streamClient(protocol, srv.URL).Complete(t.Context(), Request{})
			if err == nil || !strings.Contains(err.Error(), "request failed") || text != "" || u.PromptTokens != 19 || u.CompletionTokens != 2 {
				t.Fatalf("text=%q usage=%+v err=%v", text, u, err)
			}
		})
	}
}

func TestResponsesFinalOutputWithoutDeltas(t *testing.T) {
	body := "event: response.completed\r\ndata: {\r\ndata: \"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"text\":\"hello\"}]},{\"type\":\"function_call\",\"id\":\"item\",\"call_id\":\"call\",\"name\":\"status\",\"arguments\":\"{}\"}]}}\r\n\r\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, body) }))
	defer srv.Close()
	var text strings.Builder
	msg, _, err := NewResponses(srv.URL, "key").Stream(t.Context(), Request{}, func(s string) { text.WriteString(s) }, nil, nil)
	if err != nil || msg.Content != "hello" || text.String() != "hello" || len(msg.ToolCalls) != 1 || msg.ToolCalls[0].ID != "call" {
		t.Fatalf("msg=%+v text=%q err=%v", msg, text.String(), err)
	}
}

func TestChatFinishReasonAllowsCleanClose(t *testing.T) {
	srv := sseServer(t, `data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`)
	defer srv.Close()
	msg, _, err := New(srv.URL, "test-key").Stream(t.Context(), Request{}, nil, nil, nil)
	if err != nil || msg.Content != "ok" {
		t.Fatalf("msg=%+v err=%v", msg, err)
	}
}

func TestToolArgumentsMustBeObjects(t *testing.T) {
	for _, args := range []string{"null", "[]", `"text"`, "42"} {
		if validToolCallArgs(args) {
			t.Fatalf("accepted non-object: %s", args)
		}
	}
	if !validToolCallArgs("{}") {
		t.Fatal("rejected empty object")
	}
}
