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
}

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
	fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
	fmt.Fprint(w, "data: [DONE]\n\n")
}

// Contains reports whether a request body mentions s. JSON escaping is
// undone first, so s can be plain text.
func (r Request) Contains(s string) bool {
	quoted, _ := json.Marshal(s)
	return strings.Contains(r.Body, strings.Trim(string(quoted), `"`))
}
