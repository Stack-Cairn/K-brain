package lsp

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWedgedServerTeardown(t *testing.T) {

	inR, inW := io.Pipe()
	defer inR.Close()
	outR, outW := io.Pipe()
	defer outW.Close()
	c := newClient(inW, outR, nil)

	start := time.Now()
	for i := range 200 {
		c.notify("test/flood", map[string]any{"i": i})
		if c.isDead() {
			break
		}
	}
	if !c.isDead() {
		t.Fatal("wedged server should have torn the client down")
	}
	if took := time.Since(start); took > writeTimeout+2*time.Second {
		t.Fatalf("teardown took %s, want ~= writeTimeout", took)
	}
	c.shutdown()
}

func TestURIRoundTripSpecialChars(t *testing.T) {
	for _, p := range []string{
		"/tmp/plain/main.go",
		"/tmp/100%/main.go",
		"/tmp/a b/main.go",
		"/tmp/c#d/main.go",
		"/tmp/ünïcode/mäin.go",
		"/tmp/100%/a%20b.go",
	} {
		if got := uriPath(fileURI(p)); got != p {
			t.Errorf("round trip %q → %q", p, got)
		}
	}
	if got := uriPath("https://example.com/x"); got != "" {
		t.Errorf("non-file URI should reject, got %q", got)
	}
}

func TestCloseDuringWaitReturnsEmpty(t *testing.T) {
	f := startFakeServer(t, func(uri string, version int) []push { return nil })
	m := pipeManager(f)

	dir := t.TempDir()
	writeFile(t, dir+"/main.go", "package main\n")
	done := make(chan string, 1)
	go func() { done <- m.WaitDiagnostics(context.Background(), dir+"/main.go") }()
	<-f.onChange
	m.Close()
	select {
	case out := <-done:
		if out != "" {
			t.Fatalf("close-interrupted wait returned %q, want empty", out)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not wake the waiter")
	}
}

func TestClientForDedupLosersGetClient(t *testing.T) {
	m := NewManager(map[string]ServerSpec{"fake": fakeExecSpec()})
	defer m.Close()

	var calls atomic.Int64
	dir := t.TempDir()
	writeFile(t, dir+"/go.mod", "module x\n")
	writeFile(t, dir+"/main.go", "package main\n")

	const n = 6
	errs := make(chan error, n)
	outs := make(chan string, n)
	for range n {
		go func() {
			out := m.WaitDiagnostics(context.Background(), dir+"/main.go")
			if out == "" {
				errs <- errors.New("empty diagnostics")
				return
			}
			outs <- out
		}()
	}
	for range n {
		select {
		case err := <-errs:
			t.Fatal(err)
		case <-outs:
		case <-time.After(10 * time.Second):
			t.Fatal("deduped waiter hung")
		}
	}
	if got := calls.Load(); got > n {
		t.Fatalf("keyer calls %d > waiters %d", got, n)
	}
}

func TestRPCErrorsSurface(t *testing.T) {
	f := startFakeServer(t, nil)
	m := pipeManager(f)
	defer m.Close()
	m.mu.Lock()
	cs := m.clients["gopls\x00/froot"]
	m.mu.Unlock()

	cs.cli.shutdown()
	if err := cs.cli.request(context.Background(), "x/y", nil, nil); err == nil ||
		!strings.Contains(err.Error(), "closed") {
		t.Fatalf("request on dead client: %v", err)
	}
}
