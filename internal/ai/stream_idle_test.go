package ai

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestIdleOr(t *testing.T) {
	if got := idleOr(0); got != defaultStreamIdleTimeout {
		t.Fatalf("idleOr(0) = %s, want default %s", got, defaultStreamIdleTimeout)
	}
	if got := idleOr(-1); got != 0 {
		t.Fatalf("idleOr(-1) = %s, want 0 (disabled)", got)
	}
	if got := idleOr(5 * time.Second); got != 5*time.Second {
		t.Fatalf("idleOr(5s) = %s, want 5s", got)
	}
}

// blockingReader blocks on Read until its context is cancelled, then returns
// that context's error — modeling a half-open connection that delivers nothing.
type blockingReader struct {
	ctx context.Context
}

func (r blockingReader) Read(p []byte) (int, error) {
	<-r.ctx.Done()
	return 0, r.ctx.Err()
}

func TestIdleStreamFiresOnStall(t *testing.T) {
	sctx, guard := newIdleStream(context.Background(), 20*time.Millisecond, "TestProvider")
	defer guard.close()
	r := guard.wrap(blockingReader{ctx: sctx})

	done := make(chan error, 1)
	go func() {
		_, err := r.Read(make([]byte, 16))
		done <- err
	}()

	select {
	case err := <-done:
		var stall errStreamIdle
		if !errors.As(err, &stall) {
			t.Fatalf("stalled read returned %v, want errStreamIdle", err)
		}
		if !strings.Contains(err.Error(), "TestProvider") {
			t.Fatalf("error %q does not name the provider", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("idle watchdog did not fire within 2s")
	}
}

// errStreamIdle must stay retryable: it must not satisfy errors.Is for the
// context sentinels the retry policy refuses to retry.
func TestErrStreamIdleIsRetryable(t *testing.T) {
	err := error(errStreamIdle{idle: time.Second, provider: "X"})
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("errStreamIdle must not wrap a context error, or it would be treated as non-retryable")
	}
	if !retryable(err) {
		t.Fatal("errStreamIdle should be retryable")
	}
}

// A caller cancellation of the parent context must surface as the context error,
// not be masked as an idle stall.
func TestIdleStreamParentCancelPassesThrough(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	sctx, guard := newIdleStream(parent, time.Hour, "TestProvider")
	defer guard.close()
	r := guard.wrap(blockingReader{ctx: sctx})

	done := make(chan error, 1)
	go func() {
		_, err := r.Read(make([]byte, 16))
		done <- err
	}()

	cancelParent()

	select {
	case err := <-done:
		var stall errStreamIdle
		if errors.As(err, &stall) {
			t.Fatal("parent cancel was masked as an idle stall")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("parent cancel returned %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("parent cancel did not unblock the read")
	}
}

// progressingReader yields one byte per Read up to total, pausing between each.
type progressingReader struct {
	remaining int
	pause     time.Duration
}

func (r *progressingReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, io.EOF
	}
	time.Sleep(r.pause)
	r.remaining--
	p[0] = 'x'
	return 1, nil
}

// Steady progress, each step shorter than the idle window, must never trip the
// watchdog even when the total run far exceeds it.
func TestIdleStreamResetsOnProgress(t *testing.T) {
	_, guard := newIdleStream(context.Background(), 40*time.Millisecond, "TestProvider")
	defer guard.close()
	r := guard.wrap(&progressingReader{remaining: 8, pause: 10 * time.Millisecond})

	buf := make([]byte, 1)
	for {
		n, err := r.Read(buf)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("progressing read failed: %v", err)
		}
		if n != 1 {
			t.Fatalf("read %d bytes, want 1", n)
		}
	}
}

func TestIdleStreamDisabledPassthrough(t *testing.T) {
	ctx := context.Background()
	sctx, guard := newIdleStream(ctx, 0, "TestProvider")
	defer guard.close()
	if sctx != ctx {
		t.Fatal("disabled watchdog should return the parent context unchanged")
	}
	src := strings.NewReader("hello")
	if got := guard.wrap(src); got != io.Reader(src) {
		t.Fatal("disabled watchdog should return the reader unchanged")
	}
}

// End-to-end: an OpenAI client pointed at a server that sends headers then
// stalls forever must fail with a stall error rather than hang.
func TestOpenAIStreamIdleTimeoutEndToEnd(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-release // never send a body; hold the connection open
	}))
	defer srv.Close()
	defer once.Do(func() { close(release) })

	c := New(srv.URL, "test-key")
	c.MaxRetries = 1 // a single attempt: surface the stall without retrying
	c.StreamIdle = 50 * time.Millisecond

	done := make(chan error, 1)
	go func() {
		_, _, err := c.Stream(context.Background(), Request{Model: "test"}, nil, nil, nil)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("stalled stream returned nil error")
		}
		if !strings.Contains(err.Error(), "stalled") {
			t.Fatalf("got %v, want a stall error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OpenAI stream did not time out on a stalled server")
	}
}
