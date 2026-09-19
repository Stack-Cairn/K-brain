package ai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func retryClient(t *testing.T, protocol, endpoint string, attempts int) Client {
	t.Helper()
	api := map[string]string{"chat": APIChatCompletions, "responses": APIResponses, "anthropic": APIMessages}[protocol]
	c, err := NewClient(ClientOptions{API: api, BaseURL: endpoint, APIKey: "key", MaxRetries: attempts,
		Cache: CacheOptions{SessionAffinity: true}})
	if err != nil {
		t.Fatal(err)
	}
	c.SetCacheKey("session")
	return c.Clone()
}

func retryRequest(ctx context.Context, c Client, stream bool) (string, Usage, error) {
	req := Request{Model: "model", Messages: []Message{{Role: "user", Content: "hello"}}}
	if stream {
		msg, usage, err := c.Stream(ctx, req, nil, nil, nil)
		return msg.Content, usage, err
	}
	return c.Complete(ctx, req)
}

func TestProtocolsRetryTemporaryHTTPFailures(t *testing.T) {
	noSleep(t)
	for _, protocol := range []string{"chat", "responses", "anthropic"} {
		for _, stream := range []bool{false, true} {
			for _, status := range []int{408, 409, 429, 503, 529} {
				t.Run(fmt.Sprintf("%s/stream=%v/status=%d", protocol, stream, status), func(t *testing.T) {
					var calls atomic.Int32
					var original string
					srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						body, err := io.ReadAll(r.Body)
						if err != nil {
							t.Error(err)
						}
						attempt := calls.Add(1)
						if attempt == 1 {
							original = string(body)
						} else if original != string(body) {
							t.Error("retry changed request payload")
						}
						path := map[string]string{"chat": "/chat/completions", "responses": "/responses", "anthropic": "/messages"}[protocol]
						if r.URL.Path != path || r.Header.Get("Authorization") != "Bearer key" || r.Header.Get("X-Session-Id") != "session" || (r.Header.Get("Accept") == "text/event-stream") != stream {
							t.Errorf("request metadata lost: path=%s headers=%v", r.URL.Path, r.Header)
						}
						if protocol == "anthropic" && (r.Header.Get("X-Api-Key") != "key" || r.Header.Get("Anthropic-Version") != "2023-06-01") {
							t.Error("Anthropic headers lost")
						}
						if attempt < 3 {
							w.Header().Set("Retry-After-Ms", "25")
							w.WriteHeader(status)
							fmt.Fprint(w, `{"error":{"message":"temporary"}}`)
							return
						}
						contentResponse(w, protocol, stream)
					}))
					defer srv.Close()
					client := retryClient(t, protocol, srv.URL, 3)
					var events []RetryEvent
					client.(interface{ SetOnRetry(func(RetryEvent)) }).SetOnRetry(func(event RetryEvent) { events = append(events, event) })
					text, _, err := retryRequest(t.Context(), client, stream)
					if err != nil || text != "ok" || calls.Load() != 3 || len(events) != 2 {
						t.Fatalf("text=%q err=%v calls=%d events=%v", text, err, calls.Load(), events)
					}
					for i, event := range events {
						if event.Attempt != i+1 || event.Max != 3 || event.Delay != 25*time.Millisecond {
							t.Fatalf("wrong retry event: %+v", event)
						}
					}
				})
			}
		}
	}
}

func TestProtocolsHonorHTTPRetryDecisions(t *testing.T) {
	noSleep(t)
	for _, protocol := range []string{"chat", "responses", "anthropic"} {
		for _, stream := range []bool{false, true} {
			for _, tc := range []struct {
				name     string
				status   int
				headers  http.Header
				attempts int
				want     int
			}{
				{"authentication", 401, nil, 3, 1},
				{"context limit", 413, nil, 3, 1},
				{"bad input", 400, nil, 3, 1},
				{"disabled", 503, nil, 1, 1},
				{"exhausted", 503, nil, 3, 3},
				{"default attempts", 503, nil, 0, DefaultMaxAttempts},
				{"server says no", 503, http.Header{"X-Should-Retry": {"false"}}, 3, 1},
				{"server says yes", 400, http.Header{"X-Should-Retry": {"true"}}, 3, 3},
				{"long delay", 429, http.Header{"Retry-After": {"120"}}, 3, 1},
			} {
				t.Run(fmt.Sprintf("%s/stream=%v/%s", protocol, stream, tc.name), func(t *testing.T) {
					var calls atomic.Int32
					srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						calls.Add(1)
						for key, values := range tc.headers {
							w.Header()[key] = values
						}
						w.WriteHeader(tc.status)
						fmt.Fprint(w, "test failure")
					}))
					defer srv.Close()
					_, _, err := retryRequest(t.Context(), retryClient(t, protocol, srv.URL, tc.attempts), stream)
					var httpErr *HTTPError
					if !errors.As(err, &httpErr) || httpErr.StatusCode != tc.status || calls.Load() != int32(tc.want) {
						t.Fatalf("err=%v calls=%d want=%d", err, calls.Load(), tc.want)
					}
					if tc.name == "long delay" && !strings.Contains(err.Error(), "exceeds automatic retry limit") {
						t.Fatalf("missing delay explanation: %v", err)
					}
				})
			}
		}
	}
}

func TestProtocolsCancelDuringRetryWait(t *testing.T) {
	for _, protocol := range []string{"chat", "responses", "anthropic"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", protocol, stream), func(t *testing.T) {
				var calls atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					calls.Add(1)
					w.Header().Set("Retry-After", "60")
					w.WriteHeader(429)
				}))
				defer srv.Close()
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				client := retryClient(t, protocol, srv.URL, 3)
				client.(interface{ SetOnRetry(func(RetryEvent)) }).SetOnRetry(func(RetryEvent) { cancel() })
				started := time.Now()
				_, _, err := retryRequest(ctx, client, stream)
				if !errors.Is(err, context.Canceled) || calls.Load() != 1 || time.Since(started) > time.Second {
					t.Fatalf("cancel failed: %v calls=%d elapsed=%s", err, calls.Load(), time.Since(started))
				}
				_, _, err = retryRequest(ctx, client, stream)
				if !errors.Is(err, context.Canceled) || calls.Load() != 1 {
					t.Fatalf("cancelled request attempted: %v calls=%d", err, calls.Load())
				}
			})
		}
	}
}

func TestRetryHeaderParsing(t *testing.T) {
	for _, tc := range []struct {
		headers http.Header
		want    time.Duration
		found   bool
	}{
		{http.Header{"Retry-After": {"0.125"}}, 125 * time.Millisecond, true},
		{http.Header{"Retry-After": {"5"}, "Retry-After-Ms": {"12.5"}}, 12500 * time.Microsecond, true},
		{http.Header{"Retry-After": {"0"}}, 0, true},
		{http.Header{"Retry-After": {"3"}, "Retry-After-Ms": {"NaN"}}, 3 * time.Second, true},
		{http.Header{"Retry-After": {"Inf"}}, 0, false},
		{http.Header{"Retry-After-Ms": {"-1"}}, 0, false},
		{nil, 0, false},
	} {
		err := newHTTPError(&http.Response{StatusCode: 429, Header: tc.headers}, "body")
		if err.RetryAfter != tc.want || err.RetryAfterSet != tc.found || tc.found && retryDelay(1, err) != tc.want {
			t.Fatalf("headers=%v got=%+v", tc.headers, err)
		}
	}
	for _, header := range []string{"Retry-After", "Retry-After-Ms"} {
		err := newHTTPError(&http.Response{StatusCode: 429, Header: http.Header{header: {"1e30"}}}, "body")
		if err.RetryAfter <= maxRetryAfter || retryable(err) {
			t.Fatalf("overflowed delay: %+v", err)
		}
	}
	if retryable(&HTTPError{}) || retryable(&HTTPError{Status: "not a status"}) {
		t.Fatal("malformed HTTP status should not retry")
	}
	if d := backoff(10000); d < 20*time.Second || d > 25*time.Second {
		t.Fatalf("overflowed backoff: %s", d)
	}
}

func TestChatUsageAlonePreventsRetry(t *testing.T) {
	noSleep(t)
	for _, raw := range []string{`{}`, `{"prompt_cache_hit_tokens":17}`, `{"prompt_cache_write_tokens":9}`, `{"prompt_tokens_details":{"cached_tokens":3}}`} {
		t.Run(raw, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				fmt.Fprintf(w, "data: {\"usage\":%s}\n\n", raw)
			}))
			defer srv.Close()
			_, usage, err := retryRequest(t.Context(), retryClient(t, "chat", srv.URL, 3), true)
			if !errors.Is(err, io.ErrUnexpectedEOF) || calls.Load() != 1 {
				t.Fatalf("retried after usage: calls=%d err=%v", calls.Load(), err)
			}
			if raw != `{}` && reflect.DeepEqual(usage, Usage{}) {
				t.Fatal("lost usage")
			}
		})
	}
}

func TestProtocolsRetryDroppedHTTPConnections(t *testing.T) {
	noSleep(t)
	for _, protocol := range []string{"chat", "responses", "anthropic"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", protocol, stream), func(t *testing.T) {
				var calls atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					if calls.Add(1) < 3 {
						conn, _, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Error(err)
							return
						}
						_ = conn.Close()
						return
					}
					contentResponse(w, protocol, stream)
				}))
				defer srv.Close()
				text, _, err := retryRequest(t.Context(), retryClient(t, protocol, srv.URL, 3), stream)
				if err != nil || text != "ok" || calls.Load() != 3 {
					t.Fatalf("dropped request not recovered: text=%q err=%v calls=%d", text, err, calls.Load())
				}
			})
		}
	}
}

func TestProtocolStreamProgressPreventsRetry(t *testing.T) {
	noSleep(t)
	prefixes := map[string][]string{
		"chat": {
			`{"choices":[{"delta":{"content":"partial"}}]}`,
			`{"choices":[{"delta":{"reasoning_content":"thinking"}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call","function":{"name":"test","arguments":"{}"}}]}}]}`,
			`{"usage":{"prompt_tokens":17,"completion_tokens":3}}`,
		},
		"responses": {
			`{"type":"response.output_text.delta","delta":"partial"}`,
			`{"type":"response.reasoning_summary_text.delta","delta":"thinking"}`,
			`{"type":"response.output_item.added","item":{"type":"function_call","id":"item","call_id":"call","name":"test","arguments":"{}"}}`,
			`{"type":"response.created","response":{"usage":{"input_tokens":17,"output_tokens":3}}}`,
		},
		"anthropic": {
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"partial"}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"thinking"}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call","name":"test","input":{}}}`,
			`{"type":"message_start","message":{"usage":{"input_tokens":17,"output_tokens":3}}}`,
		},
	}
	for protocol, bodies := range prefixes {
		for i, body := range bodies {
			for _, callbacks := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%d/callbacks=%v", protocol, i, callbacks), func(t *testing.T) {
					var calls atomic.Int32
					srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						calls.Add(1)
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprint(w, streamFrames(body))
					}))
					defer srv.Close()
					client := retryClient(t, protocol, srv.URL, 3)
					var text, think strings.Builder
					var onText, onThink func(string)
					var onTool func(string, string, string)
					if callbacks {
						onText = func(s string) { text.WriteString(s) }
						onThink = func(s string) { think.WriteString(s) }
						onTool = func(string, string, string) {}
					}
					msg, usage, err := client.Stream(t.Context(), Request{Model: "model"}, onText, onThink, onTool)
					if !errors.Is(err, io.ErrUnexpectedEOF) || calls.Load() != 1 || len(msg.ToolCalls) != 0 {
						t.Fatalf("retried generated output: err=%v calls=%d msg=%+v", err, calls.Load(), msg)
					}
					if callbacks && (i == 0 && text.String() != "partial" || i == 1 && think.String() != "thinking") {
						t.Fatalf("stream output changed: text=%q think=%q", text.String(), think.String())
					}
					if i == 3 && (usage.PromptTokens != 17 || usage.CompletionTokens != 3) {
						t.Fatalf("lost usage: %+v", usage)
					}
				})
			}
		}
	}
}

func TestProtocolsDoNotRetryInvalidCompletionJSON(t *testing.T) {
	noSleep(t)
	for _, protocol := range []string{"chat", "responses", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				fmt.Fprint(w, "{invalid}")
			}))
			defer srv.Close()
			_, _, err := retryRequest(t.Context(), retryClient(t, protocol, srv.URL, 3), false)
			if err == nil || calls.Load() != 1 {
				t.Fatalf("invalid JSON retried: err=%v calls=%d", err, calls.Load())
			}
		})
	}
}
