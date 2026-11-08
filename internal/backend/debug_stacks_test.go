package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/session"
	"github.com/Stack-Cairn/K-brain/internal/stackdump"
)

func TestDebugStacksAnswersWhileTheServerIsWedged(t *testing.T) {
	dir := t.TempDir()
	prev := writeStacks
	writeStacks = func(reason string) (string, error) { return stackdump.WriteIn(dir, reason) }
	t.Cleanup(func() { writeStacks = prev })

	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	backendServer, server := newTestServer(t, store, t.TempDir(), &scriptedClient{response: "ok"})
	backendServer.mu.Lock()
	defer backendServer.mu.Unlock()

	var got struct {
		Path string `json:"path"`
	}
	within(t, 10*time.Second, "GET /v1/debug/stacks with the server lock held", func() {
		resp, err := http.Get(server.URL + "/v1/debug/stacks")
		if err != nil {
			t.Error(err)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("status %d", resp.StatusCode)
			return
		}
		if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
			t.Error(err)
		}
	})
	if filepath.Dir(got.Path) != dir {
		t.Fatalf("dump at %q, want it in %q", got.Path, dir)
	}
	raw, err := os.ReadFile(got.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "GET /v1/debug/stacks") || !strings.Contains(string(raw), "goroutine ") {
		t.Fatalf("dump lacks its reason or stacks:\n%.400s", raw)
	}
}

func TestDebugStacksNeedsTheTokenAndALoopbackClient(t *testing.T) {
	writes := atomic.Int32{}
	prev := writeStacks
	writeStacks = func(string) (string, error) { writes.Add(1); return "x", nil }
	t.Cleanup(func() { writeStacks = prev })

	s := &Server{token: "secret"}
	req := httptest.NewRequest(http.MethodGet, "/v1/debug/stacks", nil)
	req.RemoteAddr = "127.0.0.1:5000"
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("without the token: status %d, want 401", w.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/debug/stacks", nil)
	req.Header.Set("Authorization", "Bearer secret")
	req.RemoteAddr = "192.0.2.10:5000"
	w = httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("from a remote client: status %d, want 403", w.Code)
	}

	for _, addr := range []string{"127.0.0.1:5000", "[::1]:5000"} {
		req = httptest.NewRequest(http.MethodGet, "/v1/debug/stacks", nil)
		req.Header.Set("Authorization", "Bearer secret")
		req.RemoteAddr = addr
		w = httptest.NewRecorder()
		s.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("from %s: status %d, want 200", addr, w.Code)
		}
	}
	if writes.Load() != 2 {
		t.Fatalf("%d dumps written, want 2", writes.Load())
	}
}

func TestBackendWatchdogDumpsWhenASessionLockIsStuck(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	backendServer, server := newTestServer(t, store, t.TempDir(), &scriptedClient{response: "ok"})
	created := createTestSession(t, server.URL)
	rt, err := backendServer.loadRuntimeByID(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer backendServer.releaseRuntime(rt)

	dir := t.TempDir()
	stalls := make(chan string, 4)
	wd := backendServer.Watchdog(300 * time.Millisecond)
	wd.Interval = 50 * time.Millisecond
	wd.Write = func(reason string) (string, error) { return stackdump.WriteIn(dir, reason) }
	wd.OnStall = func(path string, err error) {
		if err != nil {
			t.Error(err)
		}
		stalls <- path
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rt.mu.Lock()
	wd.Start(ctx)
	select {
	case path := <-stalls:
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), "watchdogProbe") {
			t.Fatalf("the dump should show the stuck probe:\n%.400s", raw)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no dump for a session lock held past the timeout")
	}
	rt.mu.Unlock()
	select {
	case path := <-stalls:
		t.Fatalf("one stall must dump once, got a second dump %s", path)
	case <-time.After(500 * time.Millisecond):
	}
}
