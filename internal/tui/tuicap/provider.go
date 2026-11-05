//go:build tuicap

package tuicap

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"
)

// Reply is one scripted streamed answer: its chunks are sent as separate SSE
// events, Delay apart.
type Reply struct {
	Chunks []string
	Delay  time.Duration
	// ToolCalls, when set, follow the chunks: the reply then ends asking kn
	// to run them (finish_reason "tool_calls"), and kn's next request
	// carries their results.
	ToolCalls []ToolCall
	// Reasoning chunks stream first, as reasoning_content deltas.
	Reasoning []string
}

// ToolCall is one function call a scripted reply makes. Args is its JSON.
type ToolCall struct {
	ID, Name, Args string
}

// Calls is a reply that only calls tools.
func Calls(calls ...ToolCall) Reply { return Reply{ToolCalls: calls} }

// Text is a reply streamed in one chunk.
func Text(s string) Reply { return Reply{Chunks: []string{s}} }

// Lines is a reply streamed one line per chunk, delay apart.
func Lines(delay time.Duration, lines ...string) Reply {
	chunks := make([]string, len(lines))
	for i, l := range lines {
		chunks[i] = l + "\n"
	}
	return Reply{Chunks: chunks, Delay: delay}
}

// Request is one request kn sent the fake provider.
type Request struct {
	Path   string
	Stream bool
	Body   string
}

// Provider is a fake OpenAI-compatible endpoint. Streamed requests (turns)
// take the scripted replies in order; plain requests (session titles) get a
// fixed answer and do not use one up.
type Provider struct {
	URL string
	srv *httptest.Server

	mu       sync.Mutex
	replies  []Reply
	requests []Request
}

// NewProvider starts a fake provider with the given replies.
func NewProvider(replies []Reply) *Provider {
	p := &Provider{replies: append([]Reply(nil), replies...)}
	p.srv = httptest.NewServer(http.HandlerFunc(p.serve))
	p.URL = p.srv.URL
	return p
}

// Close stops the server.
func (p *Provider) Close() { p.srv.Close() }

// Requests are the streamed requests so far, oldest first.
func (p *Provider) Requests() []Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []Request
	for _, r := range p.requests {
		if r.Stream {
			out = append(out, r)
		}
	}
	return out
}

func (p *Provider) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"object":"list","data":[{"id":"tuicap-model","object":"model"}]}`)
		return
	}
	body, _ := io.ReadAll(r.Body)
	var head struct {
		Stream bool `json:"stream"`
	}
	_ = json.Unmarshal(body, &head)

	p.mu.Lock()
	p.requests = append(p.requests, Request{Path: r.URL.Path, Stream: head.Stream, Body: string(body)})
	reply := Text("ok")
	if head.Stream && len(p.replies) > 0 {
		reply, p.replies = p.replies[0], p.replies[1:]
	}
	p.mu.Unlock()

	if !head.Stream {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"tuicap session"},"finish_reason":"stop"}]}`)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	flusher, _ := w.(http.Flusher)
	for _, chunk := range reply.Reasoning {
		content, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":%s}}]}\n\n", content)
		if flusher != nil {
			flusher.Flush()
		}
		if reply.Delay > 0 {
			select {
			case <-time.After(reply.Delay):
			case <-r.Context().Done():
				return
			}
		}
	}
	for i, chunk := range reply.Chunks {
		if i > 0 && reply.Delay > 0 {
			select {
			case <-time.After(reply.Delay):
			case <-r.Context().Done():
				return
			}
		}
		content, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%s}}]}\n\n", content)
		if flusher != nil {
			flusher.Flush()
		}
	}
	finish := "stop"
	for i, c := range reply.ToolCalls {
		var d struct {
			Choices [1]struct {
				Delta struct {
					ToolCalls [1]struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		tc := &d.Choices[0].Delta.ToolCalls[0]
		tc.Index, tc.ID, tc.Type = i, c.ID, "function"
		tc.Function.Name, tc.Function.Arguments = c.Name, c.Args
		data, _ := json.Marshal(d)
		fmt.Fprintf(w, "data: %s\n\n", data)
		finish = "tool_calls"
	}
	fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":%q}]}\n\n", finish)
	fmt.Fprint(w, "data: [DONE]\n\n")
}

// Contains reports whether a request body mentions s. JSON escaping is
// undone first, so s can be plain text.
func (r Request) Contains(s string) bool {
	quoted, _ := json.Marshal(s)
	return strings.Contains(r.Body, strings.Trim(string(quoted), `"`))
}
