package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func TestSystemPromptResolverPreservesHistoryAndMemory(t *testing.T) {
	current := "base AGENTS brain\nfirst template"
	a := New(nil, "model", 100, current, WithSystemPromptResolver(func() (string, error) { return current, nil }))
	a.memoryBlock = "\nlegacy memory"
	a.runtimeMemoryBlock = "\nruntime memory"
	a.Messages = append(a.Messages, ai.Message{Role: "user", Content: "history"})
	current = "base AGENTS brain\nreplacement template"
	for range 2 {
		if err := a.refreshSystemPrompt(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	messages := a.MessagesSnapshot()
	if len(messages) != 2 || messages[1].Content != "history" {
		t.Fatalf("history changed: %+v", messages)
	}
	if messages[0].Content != current || strings.Contains(messages[0].Content, "first template") {
		t.Fatalf("system prompt = %q", messages[0].Content)
	}
}

func TestSystemPromptResolverFailurePreservesHistory(t *testing.T) {
	failure := errors.New("prompt resources unavailable")
	a := New(nil, "model", 100, "original", WithSystemPromptResolver(func() (string, error) { return "", failure }))
	if _, err := a.Turn(t.Context(), "unsent", Events{}); !errors.Is(err, failure) {
		t.Fatalf("turn error = %v", err)
	}
	if messages := a.MessagesSnapshot(); len(messages) != 1 || messages[0].Content != "original" {
		t.Fatalf("failed resolution changed history: %+v", messages)
	}
}

func TestPromptAddendumFollowsEachRefresh(t *testing.T) {
	a := New(nil, "model", 100, "base", WithSystemPromptResolver(func() (string, error) { return "base", nil }))
	extra := "\nplugin prompt"
	a.SetPromptAddendum(func() string { return extra })
	for range 2 {
		if err := a.refreshSystemPrompt(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if got := a.MessagesSnapshot()[0].Content; got != "base\nplugin prompt" {
		t.Fatalf("system prompt = %q", got)
	}
	extra = ""
	if err := a.refreshSystemPrompt(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := a.MessagesSnapshot()[0].Content; got != "base" {
		t.Fatalf("system prompt after clearing = %q", got)
	}
	// Without a resolver the prompt is fixed; the addendum is not applied.
	fixed := New(nil, "model", 100, "fixed")
	fixed.SetPromptAddendum(func() string { return "\nignored" })
	if err := fixed.refreshSystemPrompt(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := fixed.MessagesSnapshot()[0].Content; got != "fixed" {
		t.Fatalf("fixed prompt = %q", got)
	}
}

func TestPromptAdditionAppliesOnlyToItsTurn(t *testing.T) {
	a := New(nil, "model", 100, "base", WithSystemPromptResolver(func() (string, error) { return "base", nil }))
	a.SetPromptAddendum(func() string { return "\nplugin" })
	ctx := WithPromptAddition(WithPromptAddition(context.Background(), "\nfirst"), "\nsecond")
	if err := a.refreshSystemPrompt(ctx); err != nil {
		t.Fatal(err)
	}
	if got := a.MessagesSnapshot()[0].Content; got != "base\nplugin\nfirst\nsecond" {
		t.Fatalf("system prompt = %q", got)
	}
	if err := a.refreshSystemPrompt(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := a.MessagesSnapshot()[0].Content; got != "base\nplugin" {
		t.Fatalf("system prompt of a later turn = %q", got)
	}
}
