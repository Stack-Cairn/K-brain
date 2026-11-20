package ai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// relayServer serves its web page at every path except under /v1, where the
// API lives, and counts the requests each path received.
func relayServer(t *testing.T, hits map[string]*atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n := hits[r.URL.Path]; n != nil {
			n.Add(1)
		}
		if !strings.HasPrefix(r.URL.Path, "/v1/") {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, "<!doctype html><title>relay</title>")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"content":"hi"},"finish_reason":"stop"}]}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
}

// A relay set up by its bare host, which earlier LiveAgent versions completed
// with /v1, serves its web page at /chat/completions. The request retries
// under /v1 and later requests go there directly.
func TestBareRelayHostRetriesUnderV1(t *testing.T) {
	hits := map[string]*atomic.Int32{"/chat/completions": {}, "/v1/chat/completions": {}}
	srv := relayServer(t, hits)
	defer srv.Close()

	for i := range 2 {
		msg, _, err := New(srv.URL, "k").Stream(context.Background(), Request{Model: "m"}, nil, nil, nil)
		if err != nil || msg.Content != "hi" {
			t.Fatalf("request %d: %q, %v", i+1, msg.Content, err)
		}
	}
	if root, api := hits["/chat/completions"].Load(), hits["/v1/chat/completions"].Load(); root != 1 || api != 2 {
		t.Fatalf("root got %d requests, /v1 got %d; want 1 and 2", root, api)
	}
}

// A base URL that already names a version, or a full endpoint URL, is used
// as given: its web page is reported, not retried elsewhere.
func TestVersionedOrFullURLIsNotRewritten(t *testing.T) {
	hits := map[string]*atomic.Int32{"/v2/chat/completions": {}, "/v2/v1/chat/completions": {}, "/custom": {}}
	srv := relayServer(t, hits)
	defer srv.Close()

	if _, _, err := New(srv.URL+"/v2", "k").Stream(context.Background(), Request{Model: "m"}, nil, nil, nil); err == nil || !strings.Contains(err.Error(), "expected SSE data") {
		t.Fatalf("versioned base: %v", err)
	}
	full := NewWithOptions(srv.URL+"/custom", "k", true, "", nil)
	if _, _, err := full.Stream(context.Background(), Request{Model: "m"}, nil, nil, nil); err == nil || !strings.Contains(err.Error(), "expected SSE data") {
		t.Fatalf("full URL: %v", err)
	}
	if hits["/v2/chat/completions"].Load() != 1 || hits["/v2/v1/chat/completions"].Load() != 0 || hits["/custom"].Load() != 1 {
		t.Fatalf("unexpected rewrite: %d %d %d", hits["/v2/chat/completions"].Load(), hits["/v2/v1/chat/completions"].Load(), hits["/custom"].Load())
	}
}

// An API served at the bare host keeps being requested there.
func TestBareHostAPIIsNotRewritten(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	if _, _, err := New(srv.URL, "k").Stream(context.Background(), Request{Model: "m"}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 1 || paths[0] != "/chat/completions" {
		t.Fatalf("paths = %v", paths)
	}
}
