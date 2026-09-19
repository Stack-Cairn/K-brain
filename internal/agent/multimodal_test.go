package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/hooks"
)

func TestMixedContentTokensCountTextOnce(t *testing.T) {
	image := ai.ImagePart("png", []byte("png"))
	msg := ai.Message{Content: strings.Repeat("a", 100), Parts: []ai.ContentPart{image, {Type: "text", Text: strings.Repeat("b", 100)}}}
	if got, want := EstimateTokens([]ai.Message{msg}), 4+50+ai.ImageTokens(0, 0); got != want {
		t.Fatalf("tokens=%d want=%d", got, want)
	}
}

func TestPromptHookIncludesAllTextBlocks(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	srv := textServer(t, func(_ int, _ ai.Request) string { return "ok" })
	defer srv.Close()
	ag := New(ai.New(srv.URL, "key"), "model", 100, "sys")
	var prompt string
	ag.PluginHook = func(_ context.Context, event hooks.Event) error {
		if event.Name == "UserPromptSubmit" {
			prompt = event.Prompt
		}
		return nil
	}
	parts := []ai.ContentPart{ai.ImagePart("png", []byte("png")), {Type: "text", Text: "after"}}
	if _, err := ag.TurnParts(t.Context(), "before", parts, Events{}); err != nil {
		t.Fatal(err)
	}
	if prompt != "beforeafter" {
		t.Fatalf("prompt hook lost text: %q", prompt)
	}
}
