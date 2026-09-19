package acp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/hooks"
	"github.com/Stack-Cairn/K-brain/internal/mcp"
	"github.com/Stack-Cairn/K-brain/internal/memory"
	"github.com/Stack-Cairn/K-brain/internal/session"
	acp "github.com/coder/acp-go-sdk"
)

func TestSessionStartUsesFinalIdentityAndRestoredMemory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("K_BRAIN_HOME", home)
	st, err := session.OpenProjectHome(home)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := os.WriteFile(filepath.Join(st.SessionsDir(), "acp.memory.md"), []byte("- [ ] legacy-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var events []hooks.Event
	b := NewBridge("test", func(ctx context.Context, cwd string, servers map[string]mcp.ServerConfig) (*agent.Agent, *mcp.Manager, error) {
		ag := agent.New(ai.New("http://unused", "key"), "m", 100, "sys")
		ag.PluginHook = func(_ context.Context, event hooks.Event) error {
			events = append(events, event)
			meta, _, err := st.Load(event.SessionID)
			if err != nil {
				return err
			}
			if meta.CWD != cwd || event.CWD != cwd {
				t.Errorf("hook cwd = %+v", event)
			}
			if strings.Contains(ag.Messages[0].Content, "legacy-secret") {
				t.Error("shared ACP memory injected")
			}
			return nil
		}
		return ag, mcp.NewManager(nil), nil
	}, st, false, nil)
	defer b.CloseAll()
	first, err := b.NewSession(t.Context(), acp.NewSessionRequest{Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].SessionID != string(first.SessionId) {
		t.Fatalf("start events: %+v", events)
	}
	if err := memory.Session(string(first.SessionId)).Remember("first-private"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.CloseSession(t.Context(), acp.CloseSessionRequest{SessionId: first.SessionId}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ResumeSession(t.Context(), acp.ResumeSessionRequest{SessionId: first.SessionId}); err != nil {
		t.Fatal(err)
	}
	restored := b.getSession(first.SessionId).ag
	if !strings.Contains(restored.Messages[0].Content, "first-private") {
		t.Fatal("restored memory missing")
	}
	if err := restored.StartSession(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("resume repeated startup: %d", len(events))
	}
	second, err := b.NewSession(t.Context(), acp.NewSessionRequest{Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.getSession(second.SessionId).ag.Messages[0].Content, "first-private") {
		t.Fatal("cross-project memory leak")
	}
}

func TestSessionStartFailureCleansOnlyNewSessions(t *testing.T) {
	for _, method := range []string{"new", "load", "resume"} {
		for _, cancelHook := range []bool{false, true} {
			t.Run(method+map[bool]string{false: "-error", true: "-cancel"}[cancelHook], func(t *testing.T) {
				st := testStore(t)
				cwd := t.TempDir()
				id, err := st.Create(cwd, "m", "p")
				if err != nil {
					t.Fatal(err)
				}
				if err := st.Save(id, 0, []ai.Message{{Role: "user", Content: "keep this"}}, "m", "p"); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				var manager *mcp.Manager
				b := NewBridge("test", func(context.Context, string, map[string]mcp.ServerConfig) (*agent.Agent, *mcp.Manager, error) {
					ag := agent.New(ai.New("http://unused", "key"), "m", 100, "sys")
					ag.PluginHook = func(context.Context, hooks.Event) error {
						if cancelHook {
							cancel()
							return nil
						}
						return errors.New("startup failed")
					}
					manager = mcp.NewManager(nil)
					return ag, manager, nil
				}, st, false, nil)
				defer b.CloseAll()
				if method == "new" {
					_, err = b.NewSession(ctx, acp.NewSessionRequest{Cwd: cwd})
				} else {
					err = b.restoreSession(ctx, acp.SessionId(id), cwd, nil, method == "load")
				}
				if err == nil {
					t.Fatal("startup unexpectedly succeeded")
				}
				if len(b.sessions)+len(b.starting) != 0 {
					t.Fatal("failed startup retained registration")
				}
				manager.AddServers(t.Context(), map[string]mcp.ServerConfig{"late": {Command: []string{"unused"}}})
				if len(manager.Statuses()) != 0 {
					t.Fatal("MCP manager was not closed")
				}
				metas, err := st.Recent(-1)
				if err != nil || len(metas) != 1 {
					t.Fatalf("orphan session: %v %v", metas, err)
				}
				_, msgs, err := st.Load(id)
				if err != nil || len(msgs) != 1 || msgs[0].Content != "keep this" {
					t.Fatalf("existing history changed: %v %v", msgs, err)
				}
			})
		}
	}
}
