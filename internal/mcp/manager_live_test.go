package mcp

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func waitStatus(t *testing.T, m *Manager, name string, st Status) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, s := range m.Statuses() {
			if s.Name == name && s.Status == st {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("server %s never reached %s (statuses: %+v)", name, st, m.Statuses())
}

func TestAddServersLive(t *testing.T) {
	m := newTestManager(t, map[string]ServerConfig{"docs": testCfg("docs")})
	m.Start(context.Background())
	waitReady(t, m)

	m.AddServers(context.Background(), map[string]ServerConfig{"late": testCfg("late")})
	waitStatus(t, m, "late", StatusReady)

	ts := m.Tools()
	if len(ts) != 8 {
		t.Fatalf("expected 8 tools after AddServers, got %d: %v", len(ts), toolNames(ts))
	}
	names := map[string]bool{}
	for _, s := range m.Statuses() {
		names[s.Name] = true
	}
	if !names["docs"] || !names["late"] {
		t.Fatalf("statuses missing servers: %v", names)
	}
}

func TestAddServersKeepsExisting(t *testing.T) {
	m := newTestManager(t, map[string]ServerConfig{"docs": testCfg("docs")})
	m.Start(context.Background())
	waitReady(t, m)

	m.AddServers(context.Background(), map[string]ServerConfig{"docs": testCfg("docs")})
	if got := len(m.Statuses()); got != 1 {
		t.Fatalf("duplicate add should no-op, statuses = %d", got)
	}
}

func TestRemoveServersLive(t *testing.T) {
	m := newTestManager(t, map[string]ServerConfig{"docs": testCfg("docs"), "extra": testCfg("extra")})
	m.Start(context.Background())
	waitReady(t, m)
	if got := len(m.Tools()); got != 8 {
		t.Fatalf("precondition: 8 tools, got %d", got)
	}

	m.RemoveServers("extra")
	if got := len(m.Tools()); got != 4 {
		t.Fatalf("expected 4 tools after RemoveServers, got %d", got)
	}
	for _, s := range m.Statuses() {
		if s.Name == "extra" {
			t.Fatalf("removed server still listed: %+v", s)
		}
	}
	if _, ok := m.Config("extra"); ok {
		t.Fatal("removed server still has a Config entry")
	}
	if m.Reconnect("extra") {
		t.Fatal("reconnect on a removed server should refuse")
	}
}

func TestStaleToolAfterRemoveFailsClean(t *testing.T) {
	m := newTestManager(t, map[string]ServerConfig{"docs": testCfg("docs")})
	m.Start(context.Background())
	waitReady(t, m)

	stale := m.Tools()
	m.RemoveServers("docs")

	for _, tool := range stale {
		out, err := tool.Run(context.Background(), nil)
		if err == nil && out == "" {
			t.Errorf("stale tool %s: expected an error, got silence", tool.Def.Function.Name)
		}
		if err != nil && !strings.Contains(err.Error(), "docs") {
			t.Errorf("stale tool %s error should name the server: %v", tool.Def.Function.Name, err)
		}
	}
}

func TestRemoveDuringInFlightConnect(t *testing.T) {
	release := make(chan struct{})
	m := NewManager(nil)
	t.Cleanup(m.Close)
	m.connectTransport = func(ctx context.Context, cfg ServerConfig, _ *ringBuffer) (sdkmcp.Transport, error) {
		select {
		case <-release:
			return serveTestServer(t, cfg.Command[0]), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	m.AddServers(context.Background(), map[string]ServerConfig{"churn": testCfg("churn")})

	m.RemoveServers("churn")
	close(release)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(m.Statuses()) == 0 && len(m.Tools()) == 0 {

			time.Sleep(50 * time.Millisecond)
			if len(m.Statuses()) == 0 && len(m.Tools()) == 0 {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("removed server leaked through an in-flight connect: statuses=%+v tools=%d", m.Statuses(), len(m.Tools()))
}

func TestRemoveWhileConnecting(t *testing.T) {
	m := newTestManager(t, map[string]ServerConfig{"docs": testCfg("docs")})
	m.Start(context.Background())

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 50 {
				_ = m.Tools()
				_ = m.Statuses()
				_ = m.InstructionsBlock()
			}
		})
	}

	for range 10 {
		m.AddServers(context.Background(), map[string]ServerConfig{"churn": testCfg("churn")})
		m.RemoveServers("churn")
	}
	wg.Wait()
	waitStatus(t, m, "docs", StatusReady)

	for _, s := range m.Statuses() {
		if s.Name == "churn" {
			t.Fatalf("churned server reappeared: %+v", s)
		}
	}

	if m.Reconnect("churn") {
		t.Fatal("reconnect on a removed server should refuse")
	}
	time.Sleep(20 * time.Millisecond)
	for _, s := range m.Statuses() {
		if s.Name == "churn" {
			t.Fatalf("stale watcher resurrected a removed server: %+v", s)
		}
	}
}

func TestAddAfterRemoveReconnects(t *testing.T) {
	m := newTestManager(t, map[string]ServerConfig{"docs": testCfg("docs")})
	m.Start(context.Background())
	waitReady(t, m)

	m.RemoveServers("docs")
	if got := len(m.Tools()); got != 0 {
		t.Fatalf("expected no tools after removal, got %d", got)
	}
	m.AddServers(context.Background(), map[string]ServerConfig{"docs": testCfg("docs")})
	waitStatus(t, m, "docs", StatusReady)
	if got := len(m.Tools()); got != 4 {
		t.Fatalf("expected 4 tools after re-add, got %d", got)
	}
}
