package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/hooks"
)

func TestSetModelRetainsSessionAndAccountsUsage(t *testing.T) {
	a := New(ai.New("http://unused", "key"), "old", 100, "system")
	a.Provider = "provider1"
	a.SetSessionID("session-id")
	a.SetCacheKey("explicit-cache")
	a.AddUsage(ai.Usage{PromptTokens: 100, CompletionTokens: 10})
	a.notePrompt(ai.Usage{PromptTokens: 999999})
	a.Messages = append(a.Messages, ai.Message{Role: "user", Content: "keep history"})
	a.LoadTodosJSON(`[{"id":"t1","content":"keep plan","status":"pending"}]`)
	a.Steer("queued guidance")
	registry, waits, files := a.Tasks(), a.Waits(), a.files
	client := ai.New("http://unused-new", "key")
	if err := a.SetModel(ModelConfig{Client: client, ID: "new", Name: "display", Provider: "provider2", ContextLimit: 64000, MaxTokens: 8000, Temperature: new(0.2)}); err != nil {
		t.Fatal(err)
	}
	if client.CacheKey != "explicit-cache" || a.SessionIDValue() != "session-id" {
		t.Fatal("session/cache identity changed")
	}
	if a.Tasks() != registry || a.Waits() != waits || a.files != files || len(a.Todos) != 1 || len(a.Messages) != 2 || len(a.pending) != 1 {
		t.Fatal("session state lost")
	}
	if a.lastPrompt != 0 || a.ContextLimit != 64000 || a.MaxTokens != 8000 || a.ModelName != "display" {
		t.Fatal("model configuration not applied")
	}
	a.AddUsage(ai.Usage{PromptTokens: 200, CompletionTokens: 20})
	if got := a.Usage(); got.PromptTokens != 300 || got.CompletionTokens != 30 {
		t.Fatalf("usage = %+v", got)
	}
	usage := a.ModelUsage()
	if usage["old @ provider1"].PromptTokens != 100 || usage["new @ provider2"].PromptTokens != 200 {
		t.Fatalf("model usage = %+v", usage)
	}
	delete(usage, "old @ provider1")
	if len(a.ModelUsage()) != 2 {
		t.Fatal("usage snapshot aliases state")
	}
	if err := a.SetModel(ModelConfig{ID: "invalid"}); err == nil {
		t.Fatal("nil client accepted")
	}
	if a.Model != "new" || a.Client != client {
		t.Fatal("failed selection changed model")
	}
}

func TestModelChangeAndConcurrentTurnRejectedThroughoutHooks(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "Stop"} {
		t.Run(event, func(t *testing.T) {
			srv := loopServer(t)
			defer srv.Close()
			a := New(ai.New(srv.URL, "key"), "old", 100, "system")
			a.Tools = append(a.Tools, echoTool())
			entered, release := make(chan struct{}), make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			defer unblock()
			a.PluginHook = func(ctx context.Context, e hooks.Event) error {
				if e.Name == event {
					close(entered)
					<-release
				}
				return nil
			}
			done := make(chan error, 1)
			go func() { _, err := a.Turn(t.Context(), "hello", Events{}); done <- err }()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("hook did not start")
			}
			if !a.TurnRunning() {
				t.Fatal("hook excluded from running state")
			}
			if err := a.SetModel(ModelConfig{Client: ai.New("http://unused", "key"), ID: "new"}); !errors.Is(err, ErrBusy) {
				t.Fatalf("model change = %v", err)
			}
			if _, err := a.Turn(t.Context(), "duplicate", Events{}); !errors.Is(err, ErrBusy) {
				t.Fatalf("concurrent turn = %v", err)
			}
			if err := a.ManualCompact(t.Context(), Events{}); !errors.Is(err, ErrBusy) {
				t.Fatalf("concurrent compact = %v", err)
			}
			if a.Model != "old" {
				t.Fatal("busy selection changed model")
			}
			unblock()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("turn did not finish")
			}
			if a.TurnRunning() {
				t.Fatal("running state not cleared")
			}
			if err := a.SetModel(ModelConfig{Client: ai.New("http://unused", "key"), ID: "new"}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSetModelDuringExplicitStartupDoesNotDeadlock(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	a := New(ai.New("http://unused", "key"), "old", 100, "sys")
	a.PluginHook = func(context.Context, hooks.Event) error {
		if err := a.SetModel(ModelConfig{Client: ai.New("http://unused", "key"), ID: "new"}); !errors.Is(err, ErrBusy) {
			t.Errorf("model change = %v", err)
		}
		return errors.New("hook failed")
	}
	if err := a.StartSession(t.Context()); err == nil {
		t.Fatal("startup unexpectedly succeeded")
	}
	if err := a.SetModel(ModelConfig{Client: ai.New("http://unused", "key"), ID: "new"}); err != nil {
		t.Fatal(err)
	}
}
