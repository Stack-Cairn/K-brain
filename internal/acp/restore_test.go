package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/mcp"
)

func restoreForTest(ctx context.Context, b *Bridge, id acp.SessionId, cwd string, replay bool) error {
	if replay {
		_, err := b.LoadSession(ctx, acp.LoadSessionRequest{SessionId: id, Cwd: cwd, McpServers: []acp.McpServer{}})
		return err
	}
	_, err := b.ResumeSession(ctx, acp.ResumeSessionRequest{SessionId: id, Cwd: cwd, McpServers: []acp.McpServer{}})
	return err
}

func TestRestoreSessionPreservesHistoryAndCacheIdentity(t *testing.T) {
	for _, replay := range []bool{true, false} {
		t.Run(fmt.Sprint("replay=", replay), func(t *testing.T) {
			st := testStore(t)
			dir := t.TempDir()
			id, err := st.Create(dir, "m", "p")
			if err != nil {
				t.Fatal(err)
			}
			history := []ai.Message{
				{Role: "system", Content: "old system prompt"},
				{Role: "user", Content: "remember this"},
				{Role: "assistant", Content: "remembered"},
				{Role: "system", Content: "Keep this context note"},
			}
			if err := st.Save(id, 0, history, "m", "p"); err != nil {
				t.Fatal(err)
			}
			requests := make(chan ai.Request, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req ai.Request
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				requests <- req
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"continued\"}}]}\n\ndata: [DONE]\n\n")
			}))
			t.Cleanup(srv.Close)
			f := newFixture(t, nil, st, factoryFor(srv, nil))
			f.initialize(t)
			if replay {
				_, err = f.conn.LoadSession(context.Background(), acp.LoadSessionRequest{SessionId: acp.SessionId(id), Cwd: dir, McpServers: []acp.McpServer{}})
			} else {
				_, err = f.conn.ResumeSession(context.Background(), acp.ResumeSessionRequest{SessionId: acp.SessionId(id), Cwd: dir, McpServers: []acp.McpServer{}})
			}
			if err != nil {
				t.Fatal(err)
			}
			s := f.bridge.getSession(acp.SessionId(id))
			if s.ag.SessionIDValue() != id || s.ag.WorkingDir != dir {
				t.Fatalf("restored identity: id=%q cwd=%q", s.ag.SessionIDValue(), s.ag.WorkingDir)
			}
			if got := len(f.client.snapshot()) > 0; got != replay {
				t.Fatalf("history replay = %v, want %v", got, replay)
			}
			if _, err := f.prompt(t, acp.SessionId(id), "continue"); err != nil {
				t.Fatal(err)
			}
			req := <-requests
			if req.PromptCacheKey != id {
				t.Fatalf("cache key = %q, want %q", req.PromptCacheKey, id)
			}
			var users []string
			var note bool
			for _, msg := range req.Messages {
				if msg.Role == "user" {
					users = append(users, msg.Content)
				}
				if msg.Content == "old system prompt" {
					t.Fatal("obsolete system prompt was sent to model")
				}
				if msg.Content == "Keep this context note" {
					note = true
				}
			}
			if !note || strings.Join(users, "|") != "remember this|continue" {
				t.Fatalf("model history = %+v", req.Messages)
			}
			_, saved, err := st.Load(id)
			if err != nil {
				t.Fatal(err)
			}
			if len(saved) != 6 || saved[1].Content != "remember this" || saved[4].Content != "continue" {
				t.Fatalf("stored history = %+v", saved)
			}
		})
	}
}

func TestRestoreSessionRejectsActiveWithoutFactory(t *testing.T) {
	st := testStore(t)
	srv := scriptServer(t, []step{{text: "ok"}})
	var calls atomic.Int32
	f := newFixture(t, nil, st, func(ctx context.Context, cwd string, servers map[string]mcp.ServerConfig) (*agent.Agent, *mcp.Manager, error) {
		calls.Add(1)
		return factoryFor(srv, nil)(ctx, cwd, servers)
	})
	f.initialize(t)
	dir := t.TempDir()
	id := f.newSession(t, dir)
	original := f.bridge.getSession(id)
	for _, replay := range []bool{true, false} {
		err := restoreForTest(context.Background(), f.bridge, id, dir, replay)
		if err == nil || !strings.Contains(err.Error(), "already active") {
			t.Fatalf("active restore = %v", err)
		}
		if f.bridge.getSession(id) != original || calls.Load() != 1 {
			t.Fatal("active session was replaced or factory reran")
		}
	}
	if _, err := f.prompt(t, id, "still usable"); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentRestoreCreatesOneAgent(t *testing.T) {
	st := testStore(t)
	dir := t.TempDir()
	id, err := st.Create(dir, "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	srv := scriptServer(t, []step{{text: "ok"}})
	b := NewBridge("test", func(ctx context.Context, cwd string, servers map[string]mcp.ServerConfig) (*agent.Agent, *mcp.Manager, error) {
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
		return factoryFor(srv, nil)(ctx, cwd, servers)
	}, st, false, nil)
	t.Cleanup(b.CloseAll)
	done := make(chan error, 1)
	go func() { done <- restoreForTest(context.Background(), b, acp.SessionId(id), dir, true) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("factory did not start")
	}
	err = restoreForTest(context.Background(), b, acp.SessionId(id), dir, false)
	if err == nil || !strings.Contains(err.Error(), "loading") {
		t.Fatalf("concurrent restore = %v", err)
	}
	select {
	case <-started:
		t.Fatal("factory started twice")
	default:
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("restore did not finish")
	}
}

func TestRestoreFailureCanRetry(t *testing.T) {
	st := testStore(t)
	dir := t.TempDir()
	id, err := st.Create(dir, "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	srv := scriptServer(t, []step{{text: "ok"}})
	var calls int
	b := NewBridge("test", func(ctx context.Context, cwd string, servers map[string]mcp.ServerConfig) (*agent.Agent, *mcp.Manager, error) {
		calls++
		if calls == 1 {
			return nil, nil, fmt.Errorf("factory failed")
		}
		return factoryFor(srv, nil)(ctx, cwd, servers)
	}, st, false, nil)
	t.Cleanup(b.CloseAll)
	if err := restoreForTest(context.Background(), b, acp.SessionId(id), dir, true); err == nil {
		t.Fatal("factory error ignored")
	}
	if b.getSession(acp.SessionId(id)) != nil {
		t.Fatal("failed restore registered")
	}
	if err := restoreForTest(context.Background(), b, acp.SessionId(id), dir, false); err != nil {
		t.Fatal(err)
	}
}
