package ai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// headerlessServer accepts requests and never answers the first `hang` of
// them — not even with headers — then streams a short chat reply.
func headerlessServer(t *testing.T, hang int32) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	release := make(chan struct{})
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= hang {
			select {
			case <-r.Context().Done():
			case <-release:
			}
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(func() { close(release); srv.Close() })
	return srv, &calls
}

// A provider that never sends response headers trips the idle watchdog while
// the request is still being made. That must read as a stall — retried, and
// never as context.Canceled, which the UI shows as the user interrupting.
func TestHeaderStallIsRetriedNotCanceled(t *testing.T) {
	noSleep(t)
	srv, calls := headerlessServer(t, 1)
	c := New(srv.URL, "k")
	c.StreamIdle = 50 * time.Millisecond
	var retries []RetryEvent
	c.OnRetry = func(ev RetryEvent) { retries = append(retries, ev) }
	msg, _, err := c.Stream(context.Background(), Request{Model: "m"}, nil, nil, nil)
	if err != nil {
		t.Fatalf("second attempt should succeed, got %v", err)
	}
	if msg.Content != "ok" || calls.Load() != 2 {
		t.Fatalf("content %q after %d requests", msg.Content, calls.Load())
	}
	if len(retries) != 1 {
		t.Fatalf("retries = %d, want 1", len(retries))
	}
	if idle, ok := StreamStall(retries[0].Err); !ok || idle != 50*time.Millisecond {
		t.Fatalf("retry reason %v should be a 50ms stall", retries[0].Err)
	}
}

func TestHeaderStallInEveryClient(t *testing.T) {
	noSleep(t)
	for _, name := range []string{"chat", "responses", "anthropic", "gemini"} {
		t.Run(name, func(t *testing.T) {
			srv, calls := headerlessServer(t, 1<<20)
			var retried int
			onRetry := func(ev RetryEvent) {
				if _, ok := StreamStall(ev.Err); ok {
					retried++
				}
			}
			var stream func() error
			switch name {
			case "chat":
				c := New(srv.URL, "k")
				c.StreamIdle, c.MaxRetries, c.OnRetry = 40*time.Millisecond, 2, onRetry
				stream = func() error {
					_, _, err := c.Stream(context.Background(), Request{Model: "m"}, nil, nil, nil)
					return err
				}
			case "responses":
				c := &Responses{OpenAI: New(srv.URL, "k")}
				c.StreamIdle, c.MaxRetries, c.OnRetry = 40*time.Millisecond, 2, onRetry
				stream = func() error {
					_, _, err := c.Stream(context.Background(), Request{Model: "m"}, nil, nil, nil)
					return err
				}
			case "anthropic":
				c := newAnthropicWithOptions(srv.URL, "k", false, "", nil)
				c.StreamIdle, c.MaxRetries, c.OnRetry = 40*time.Millisecond, 2, onRetry
				stream = func() error {
					_, _, err := c.Stream(context.Background(), Request{Model: "m"}, nil, nil, nil)
					return err
				}
			case "gemini":
				c := NewGeminiWithOptions(srv.URL, "k", false, "", nil)
				c.StreamIdle, c.MaxRetries, c.OnRetry = 40*time.Millisecond, 2, onRetry
				stream = func() error {
					_, _, err := c.Stream(context.Background(), Request{Model: "m"}, nil, nil, nil)
					return err
				}
			}
			err := stream()
			if errors.Is(err, context.Canceled) {
				t.Fatalf("a stall must not surface as context.Canceled: %v", err)
			}
			if _, ok := StreamStall(err); !ok {
				t.Fatalf("err = %v, want a stall", err)
			}
			if retried != 1 || calls.Load() != 2 {
				t.Fatalf("stall retries = %d over %d requests, want 1 over 2", retried, calls.Load())
			}
		})
	}
}

// The user stopping the turn while the request hangs is still a cancel.
func TestHeaderWaitCanceledByCallerStaysCanceled(t *testing.T) {
	srv, _ := headerlessServer(t, 1<<20)
	c := New(srv.URL, "k")
	c.StreamIdle = time.Hour
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, _, err := c.Stream(ctx, Request{Model: "m"}, nil, nil, nil)
	if _, ok := StreamStall(err); ok || err == nil {
		t.Fatalf("caller cancel should not read as a stall: %v", err)
	}
}
