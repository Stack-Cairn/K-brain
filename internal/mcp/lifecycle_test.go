package mcp

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func awaitLifecycle(t *testing.T, ch <-chan struct{}, operation string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", operation)
	}
}

func TestCloseCancelsAndJoinsConnectingWorkers(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	m := NewManager(map[string]ServerConfig{"slow": testCfg("slow")})
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(m.Close)
	t.Cleanup(unblock)
	var calls atomic.Int64
	m.connectTransport = func(ctx context.Context, _ ServerConfig, _ *ringBuffer) (sdkmcp.Transport, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-ctx.Done()
			close(canceled)
			<-release
		} else {
			<-ctx.Done()
		}
		return nil, ctx.Err()
	}
	for range 10 {
		m.Start(context.Background())
	}
	awaitLifecycle(t, started, "connect")
	closed := make(chan struct{}, 2)
	for range 2 {
		go func() { m.Close(); closed <- struct{}{} }()
	}
	awaitLifecycle(t, canceled, "connect cancellation")
	select {
	case <-closed:
		t.Fatal("Close returned before the transport worker exited")
	case <-time.After(20 * time.Millisecond):
	}
	unblock()
	awaitLifecycle(t, closed, "first Close")
	awaitLifecycle(t, closed, "second Close")
	if calls.Load() != 1 {
		t.Fatalf("repeated Start launched %d connections", calls.Load())
	}
	m.Start(context.Background())
	m.AddServers(context.Background(), map[string]ServerConfig{"extra": testCfg("extra")})
	if m.Reconnect("slow") || m.Enable("slow") || m.Disable("slow") || len(m.Statuses()) != 1 {
		t.Fatal("closed manager accepted lifecycle changes")
	}
}

func TestCallerCancellationClosesLiveSession(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	m := newTestManager(t, map[string]ServerConfig{"docs": testCfg("docs")})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)
	waitReady(t, m)
	s := m.servers["docs"]
	s.mu.Lock()
	sess := s.sess
	s.mu.Unlock()
	if sess == nil {
		t.Fatal("session not connected")
	}
	cancel()
	done := make(chan struct{})
	go func() { s.workers.Wait(); close(done) }()
	awaitLifecycle(t, done, "server workers")
	if len(m.Tools()) != 0 || m.Statuses()[0].Status != StatusFailed {
		t.Fatalf("canceled server retained tools or ready status: %+v", m.Statuses())
	}
	if m.Reconnect("docs") {
		t.Fatal("canceled caller was replaced with a background reconnect")
	}
	ended := make(chan struct{})
	go func() { _ = sess.Wait(); close(ended) }()
	awaitLifecycle(t, ended, "session exit")
}

func TestRemoveCancelsOldServerBeforeSameNameReplacement(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	m := NewManager(map[string]ServerConfig{"docs": testCfg("old")})
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(m.Close)
	t.Cleanup(unblock)
	m.connectTransport = func(ctx context.Context, cfg ServerConfig, _ *ringBuffer) (sdkmcp.Transport, error) {
		if cfg.Command[0] == "old" {
			close(started)
			<-ctx.Done()
			close(canceled)
			<-release
			return nil, errors.New("old connection failed")
		}
		return serveTestServer(t, "new"), nil
	}
	m.Start(context.Background())
	awaitLifecycle(t, started, "old connection")
	removed := make(chan struct{})
	go func() { m.RemoveServers("docs"); close(removed) }()
	awaitLifecycle(t, canceled, "removed connection cancellation")
	m.AddServers(context.Background(), map[string]ServerConfig{"docs": testCfg("new")})
	waitStatus(t, m, "docs", StatusReady)
	unblock()
	awaitLifecycle(t, removed, "old server removal")
	if m.Statuses()[0].Status != StatusReady || len(m.Tools()) != 4 {
		t.Fatalf("old connection overwrote its replacement: %+v", m.Statuses())
	}
}

func TestEnableInitiallyDisabledServer(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	cfg := testCfg("docs")
	cfg.Enabled = new(false)
	m := newTestManager(t, map[string]ServerConfig{"docs": cfg})
	m.Start(context.Background())
	if !m.Enable("docs") {
		t.Fatal("Enable returned false")
	}
	waitStatus(t, m, "docs", StatusReady)
	if len(m.Tools()) != 4 {
		t.Fatal("enabled server has no tools")
	}
}

func TestDisableInterruptsStartupAndAllowsEnable(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	m := NewManager(map[string]ServerConfig{"docs": testCfg("docs")})
	t.Cleanup(m.Close)
	started, canceled := make(chan struct{}), make(chan struct{})
	var attempts atomic.Int64
	m.connectTransport = func(ctx context.Context, _ ServerConfig, _ *ringBuffer) (sdkmcp.Transport, error) {
		if attempts.Add(1) == 1 {
			close(started)
			<-ctx.Done()
			close(canceled)
			return nil, ctx.Err()
		}
		return serveTestServer(t, "docs"), nil
	}
	m.Start(context.Background())
	awaitLifecycle(t, started, "startup")
	if !m.Disable("docs") {
		t.Fatal("Disable returned false")
	}
	awaitLifecycle(t, canceled, "disabled startup cancellation")
	if !m.Enable("docs") {
		t.Fatal("Enable returned false")
	}
	waitStatus(t, m, "docs", StatusReady)
	if attempts.Load() != 2 {
		t.Fatalf("unexpected connection count: %d", attempts.Load())
	}
}

func TestCloseInterruptsReconnectBackoff(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	t.Setenv("K_BRAIN_TEST_MCP_BACKOFF_MS", "60000")
	m := NewManager(map[string]ServerConfig{"docs": testCfg("docs")})
	t.Cleanup(m.Close)
	s := m.servers["docs"]
	s.kickAutoReconnect(m)
	done := make(chan struct{})
	go func() { m.Close(); close(done) }()
	awaitLifecycle(t, done, "backoff cancellation")
	if len(s.reconnect) != 0 {
		t.Fatal("close scheduled a reconnect")
	}
}
