package agent

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/hooks"
	"github.com/Stack-Cairn/K-brain/internal/memory"
	"github.com/Stack-Cairn/K-brain/internal/sandbox"
	"github.com/Stack-Cairn/K-brain/internal/session"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func TestSessionStartRetryAndModelChange(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	ag := New(ai.New("http://unused", "k"), "m", 100, "sys")
	ag.WorkingDir = t.TempDir()
	ag.SetSessionID("session-a")
	policy := &sandbox.Policy{Mode: "off", Root: ag.WorkingDir}
	ag.SandboxPolicy = policy
	calls := 0
	ag.PluginHook = func(ctx context.Context, event hooks.Event) error {
		calls++
		if event.SessionID != ag.SessionIDValue() || event.CWD != ag.WorkingDir || sandbox.FromContext(ctx) != policy {
			t.Fatalf("wrong hook context: %+v", event)
		}
		if calls == 1 {
			return errors.New("retry")
		}
		return nil
	}
	if err := ag.StartSession(t.Context()); err == nil {
		t.Fatal("expected failure")
	}
	for range 2 {
		if err := ag.StartSession(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatalf("start count = %d", calls)
	}
	if err := ag.SetModel(ModelConfig{Client: ai.New("http://unused", "k"), ID: "other-model"}); err != nil {
		t.Fatal(err)
	}
	if err := ag.StartSession(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("model change repeated SessionStart")
	}
	ag.SetSessionID("session-b")
	if err := ag.StartSession(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatal("new session did not start")
	}
}

func TestSessionMemoryRefreshAndSubagentIsolation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("K_BRAIN_HOME", home)
	st, err := session.OpenProjectHome(home)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ids := make([]string, 2)
	for i, fact := range []string{"alpha-private", "beta-private"} {
		ids[i], err = st.Create(t.TempDir(), "m", "p")
		if err != nil {
			t.Fatal(err)
		}
		if err := memory.Session(ids[i]).Remember(fact); err != nil {
			t.Fatal(err)
		}
	}
	if err := memory.Installation().Remember("global-fact"); err != nil {
		t.Fatal(err)
	}
	ag := New(ai.New("http://unused", "k"), "m", 100, "sys")
	ag.SetSessionID(ids[0])
	for range 3 {
		ag.RefreshMemory()
	}
	if strings.Count(ag.Messages[0].Content, "alpha-private") != 1 {
		t.Fatal("duplicate or missing memory")
	}
	ag.SetSessionID(ids[1])
	ag.RefreshMemory()
	if strings.Contains(ag.Messages[0].Content, "alpha-private") || !strings.Contains(ag.Messages[0].Content, "beta-private") {
		t.Fatal("session memory leaked")
	}
	if err := memory.Session(ids[1]).Forget(1); err != nil {
		t.Fatal(err)
	}
	ag.RefreshMemory()
	if strings.Contains(ag.Messages[0].Content, "beta-private") {
		t.Fatal("forgotten memory retained")
	}
	sub := ag.newSub(SubModel{})
	if err := sub.StartSession(t.Context()); err != nil {
		t.Fatal(err)
	}
	sub.RefreshMemory()
	if strings.Contains(sub.Messages[0].Content, "global-fact") || strings.Contains(sub.Messages[0].Content, "private") {
		t.Fatal("subagent inherited memory")
	}
	if sub.SessionIDValue() != ids[1] {
		t.Fatal("subagent lost hook session identity")
	}
}

func TestSessionHooksAcrossToolTurn(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	srv := loopServer(t)
	defer srv.Close()
	ag := New(ai.New(srv.URL, "k"), "m", 100, "sys")
	ag.Tools = []tools.Tool{echoTool()}
	ag.SetSessionID("session-a")
	ag.WorkingDir = t.TempDir()
	var events []string
	ag.PluginHook = func(_ context.Context, event hooks.Event) error {
		if event.SessionID != "session-a" || event.CWD != ag.WorkingDir {
			t.Errorf("hook identity: %+v", event)
		}
		events = append(events, event.Name)
		return nil
	}
	if _, err := ag.Turn(t.Context(), "hello", Events{}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(events, []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "Stop"}) {
		t.Fatalf("events = %v", events)
	}
}
