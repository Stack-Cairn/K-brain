package acp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/mcp"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func TestCloseSessionWaitsForTurnAndPersistence(t *testing.T) {
	st := testStore(t)
	started := make(chan struct{})
	cancelled := make(chan struct{})
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	probe := tools.Tool{Def: llmTool("close_probe"), Run: func(ctx context.Context, _ json.RawMessage) (string, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		<-release
		return "", ctx.Err()
	}}
	srv := scriptServer(t, []step{{toolName: "close_probe", toolArgs: `{}`}})
	f := newFixture(t, nil, st, factoryFor(srv, []tools.Tool{probe}))
	t.Cleanup(unblock)
	f.initialize(t)
	dir := t.TempDir()
	id := f.newSession(t, dir)
	original := f.bridge.getSession(id)
	promptDone := make(chan error, 1)
	go func() {
		resp, err := f.bridge.Prompt(context.Background(), acp.PromptRequest{SessionId: id, Prompt: []acp.ContentBlock{acp.TextBlock("save before close")}})
		if err == nil && resp.StopReason != acp.StopReasonCancelled {
			err = errString("expected cancelled turn")
		}
		promptDone <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("tool did not start")
	}
	closeDone := make(chan error, 1)
	go func() {
		_, err := f.bridge.CloseSession(context.Background(), acp.CloseSessionRequest{SessionId: id})
		closeDone <- err
	}()
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("close did not cancel the tool")
	}
	select {
	case <-closeDone:
		t.Fatal("close returned before the tool settled")
	default:
	}
	if f.bridge.getSession(id) != original {
		t.Fatal("closing session disappeared before persistence")
	}
	if err := restoreForTest(context.Background(), f.bridge, id, dir, false); err == nil {
		t.Fatal("restored over a closing session")
	}
	unblock()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("close did not finish")
	}
	select {
	case err := <-promptDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("prompt did not finish")
	}
	_, msgs, err := st.Load(string(id))
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) == 0 || msgs[0].Content != "save before close" {
		t.Fatalf("turn was not persisted: %+v", msgs)
	}
	if err := restoreForTest(context.Background(), f.bridge, id, dir, false); err != nil {
		t.Fatal(err)
	}
	if f.bridge.getSession(id) == original {
		t.Fatal("closed agent reused")
	}
}

func TestCloseAllCancelsSessionSetup(t *testing.T) {
	for _, method := range []string{"new", "load", "resume"} {
		t.Run(method, func(t *testing.T) {
			st := testStore(t)
			dir := t.TempDir()
			id, err := st.Create(dir, "m", "p")
			if err != nil {
				t.Fatal(err)
			}
			started := make(chan struct{})
			created := make(chan *mcp.Manager, 1)
			srv := scriptServer(t, []step{{text: "ok"}})
			b := NewBridge("test", func(ctx context.Context, cwd string, servers map[string]mcp.ServerConfig) (*agent.Agent, *mcp.Manager, error) {
				close(started)
				<-ctx.Done()
				ag, mgr, err := factoryFor(srv, nil)(ctx, cwd, servers)
				created <- mgr
				return ag, mgr, err
			}, st, false, nil)
			t.Cleanup(b.CloseAll)
			done := make(chan error, 1)
			go func() {
				if method == "new" {
					_, err := b.NewSession(context.Background(), acp.NewSessionRequest{Cwd: dir})
					done <- err
				} else {
					done <- restoreForTest(context.Background(), b, acp.SessionId(id), dir, method == "load")
				}
			}()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("setup did not start")
			}
			closed := make(chan struct{})
			go func() { b.CloseAll(); close(closed) }()
			select {
			case <-closed:
			case <-time.After(3 * time.Second):
				t.Fatal("shutdown did not cancel setup")
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("setup succeeded after shutdown")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("setup did not finish")
			}
			b.mu.Lock()
			remaining := len(b.sessions) + len(b.starting)
			b.mu.Unlock()
			if remaining != 0 {
				t.Fatalf("shutdown retained %d sessions", remaining)
			}
			mgr := <-created
			mgr.AddServers(context.Background(), map[string]mcp.ServerConfig{"after-close": {Command: []string{"unused"}, Enabled: new(false)}})
			if len(mgr.Statuses()) != 0 {
				t.Fatal("manager created during shutdown was not closed")
			}
			transcripts, err := filepath.Glob(filepath.Join(st.SessionsDir(), "*", "session.jsonl"))
			if err != nil || len(transcripts) != 1 {
				t.Fatalf("shutdown left an extra persisted session: %v, %v", transcripts, err)
			}
			if _, err := b.NewSession(context.Background(), acp.NewSessionRequest{Cwd: dir}); err == nil || !strings.Contains(err.Error(), "closed") {
				t.Fatalf("new after shutdown = %v", err)
			}
		})
	}
}
