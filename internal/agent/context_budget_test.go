package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

// The context measure is the prompt the provider last reported, which counts
// the system prompt and tool definitions, plus only the messages added after
// that request; a replaced history falls back to the estimate.
func TestContextTokensBuildsOnTheReportedPrompt(t *testing.T) {
	ag := New(nil, "m", 100, "sys")
	ag.Messages = append(ag.Messages, ai.Message{Role: "user", Content: "hi"})
	sent := len(ag.Messages)
	ag.notePrompt(ai.Usage{PromptTokens: 800}, sent)
	if got := ag.ContextTokens(); got != 800 {
		t.Fatalf("right after the request: %d, want the reported 800", got)
	}
	reply := ai.Message{Role: "assistant", Content: strings.Repeat("x", 400)}
	ag.Messages = append(ag.Messages, reply)
	if got, want := ag.ContextTokens(), 800+EstimateTokens([]ai.Message{reply}); got != want {
		t.Fatalf("after the reply: %d, want %d", got, want)
	}
	if got := ag.ContextTokensNow(); got != ag.ContextTokens() {
		t.Fatalf("the footer reads %d, the agent %d", got, ag.ContextTokens())
	}

	ag.RestoreMessages(ag.Messages[:1])
	if got, want := ag.ContextTokens(), EstimateTokens(ag.Messages); got != want {
		t.Fatalf("after a restore: %d, want the estimate %d", got, want)
	}
}

// budgetServer answers each turn request with "ok", recording its output
// limit, and counts the compaction summaries asked for.
func budgetServer(t *testing.T, limits *[]int, summaries *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Stream              bool `json:"stream"`
			MaxTokens           int  `json:"max_tokens"`
			MaxCompletionTokens int  `json:"max_completion_tokens"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if !req.Stream {
			*summaries++
			w.Write([]byte(`{"choices":[{"message":{"content":"summary of prior work"}}]}`))
			return
		}
		*limits = append(*limits, max(req.MaxTokens, req.MaxCompletionTokens))
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
}

// A model whose output limit is large next to its window used to compact once
// history plus that whole limit passed the window: here at 75%, far below the
// 90% threshold. The request now asks for the room that is left instead.
func TestLargeOutputLimitShrinksInsteadOfCompacting(t *testing.T) {
	var limits []int
	var summaries int
	srv := budgetServer(t, &limits, &summaries)
	defer srv.Close()

	ag := New(ai.New(srv.URL, "k"), "m", 64_000, "sys")
	ag.ContextLimit = 200_000
	for range 10 {
		ag.Messages = append(ag.Messages, ai.Message{Role: "user", Content: strings.Repeat("x", 60_000)})
	}
	before := EstimateTokens(ag.Messages)
	var compacted bool
	if _, err := ag.Turn(context.Background(), "go on", Events{OnCompactStart: func(int, int) { compacted = true }}); err != nil {
		t.Fatal(err)
	}
	if compacted || summaries != 0 {
		t.Fatalf("compacted at %d of %d tokens", before, ag.ContextLimit)
	}
	if len(limits) != 1 || limits[0] >= 64_000 || limits[0] < minOutputReserve {
		t.Fatalf("output limits sent: %v, want one below 64000 and at least %d", limits, minOutputReserve)
	}
}

// Once even the reserve no longer fits, the conversation is compacted before
// the request, as before.
func TestNoRoomForTheReserveStillCompacts(t *testing.T) {
	var limits []int
	var summaries int
	srv := budgetServer(t, &limits, &summaries)
	defer srv.Close()

	ag := New(ai.New(srv.URL, "k"), "m", 64_000, "sys")
	ag.ContextLimit = 200_000
	ag.CompactThreshold = 0.95
	for range 10 {
		// About 93% of the window: under the threshold, but less than the
		// 16,384-token reserve is left.
		ag.Messages = append(ag.Messages, ai.Message{Role: "user", Content: strings.Repeat("x", 74_400)})
	}
	before := EstimateTokens(ag.Messages)
	var compacted bool
	if _, err := ag.Turn(context.Background(), "go on", Events{OnCompactStart: func(int, int) { compacted = true }}); err != nil {
		t.Fatal(err)
	}
	if !compacted || summaries != 1 {
		t.Fatalf("no compaction at %d of %d tokens", before, ag.ContextLimit)
	}
	if len(limits) != 1 || limits[0] != 64_000 {
		t.Fatalf("after compaction the full output limit fits again, sent %v", limits)
	}
}
