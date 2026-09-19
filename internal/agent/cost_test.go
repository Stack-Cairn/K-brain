package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func TestMaybeCompactUsesRealUsage(t *testing.T) {
	var summaryCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ai.Request
		json.NewDecoder(r.Body).Decode(&req)
		if !req.Stream {
			summaryCalls.Add(1)
			w.Write([]byte(`{"choices":[{"message":{"content":"folded"}}],"usage":{"prompt_tokens":100,"completion_tokens":10}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`+"\n\n")

		fmt.Fprint(w, `data: {"choices":[],"usage":{"prompt_tokens":400000,"completion_tokens":5}}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	ag := New(ai.New(srv.URL, "k"), "m", 100, "sys")
	ag.ContextLimit = 1_000_000
	ag.CompactThreshold = 0.30

	for range 6 {
		ag.Messages = append(ag.Messages,
			ai.Message{Role: "user", Content: strings.Repeat("q", 2000)},
			ai.Message{Role: "assistant", Content: strings.Repeat("a", 2000)},
		)
	}

	if est := EstimateTokens(ag.Messages); est >= 300_000 {
		t.Fatalf("test setup: estimate %d should be well under 300k for the real-usage path to matter", est)
	}
	if _, err := ag.Turn(context.Background(), "continue", Events{}); err != nil {
		t.Fatal(err)
	}
	if summaryCalls.Load() == 0 {
		t.Fatal("real prompt size 400k (>30% of 1M) should have triggered compaction despite the low text estimate")
	}

	if ag.lastPrompt != 0 {
		t.Fatalf("lastPrompt should reset after compaction, got %d", ag.lastPrompt)
	}
}

func TestLastPromptIgnoresFannedInUsage(t *testing.T) {
	ag := New(nil, "m", 100, "sys")
	ag.AddUsage(ai.Usage{PromptTokens: 900_000})
	if ag.lastPrompt != 0 {
		t.Fatalf("AddUsage must not set lastPrompt, got %d", ag.lastPrompt)
	}
	ag.notePrompt(ai.Usage{PromptTokens: 1234})
	ag.notePrompt(ai.Usage{})
	if ag.lastPrompt != 1234 {
		t.Fatalf("notePrompt should set lastPrompt to 1234, got %d", ag.lastPrompt)
	}
}

func TestMaybeCompactEstimateFallback(t *testing.T) {
	var summaryCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ai.Request
		json.NewDecoder(r.Body).Decode(&req)
		if !req.Stream {
			summaryCalls.Add(1)
			w.Write([]byte(`{"choices":[{"message":{"content":"folded"}}]}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	ag := New(ai.New(srv.URL, "k"), "m", 100, "sys")
	ag.ContextLimit = 1000
	ag.CompactThreshold = 0.30
	for range 6 {
		ag.Messages = append(ag.Messages,
			ai.Message{Role: "user", Content: strings.Repeat("q", 2000)},
			ai.Message{Role: "assistant", Content: strings.Repeat("a", 2000)},
		)
	}
	if _, err := ag.Turn(context.Background(), "continue", Events{}); err != nil {
		t.Fatal(err)
	}
	if summaryCalls.Load() == 0 {
		t.Fatal("with no usage reported, the estimate (>30% of the tiny window) should still trigger compaction")
	}
}

func TestDoomLoopGuard(t *testing.T) {
	ag := New(nil, "m", 100, "sys")
	mk := func(id, name, args string) ai.ToolCall {
		var tc ai.ToolCall
		tc.ID = id
		tc.Function.Name = name
		tc.Function.Arguments = args
		return tc
	}

	batch := []ai.ToolCall{mk("a", "bash", `{"command":"git status"}`), mk("b", "bash", `{"command":"git status"}`), mk("c", "bash", `{"command":"git status"}`)}
	refused := ag.markDoomLoops(batch)
	if refused[0] || refused[1] || !refused[2] {
		t.Fatalf("3rd identical call should be refused, 1st/2nd allowed: %v", refused)
	}

	if r := ag.markDoomLoops([]ai.ToolCall{mk("d", "bash", `{"command":"git status"}`)}); !r[0] {
		t.Fatal("4th identical call should also be refused")
	}

	if r := ag.markDoomLoops([]ai.ToolCall{mk("e", "read", `{"path":"x"}`)}); r[0] {
		t.Fatal("a different call must reset the run")
	}
	if r := ag.markDoomLoops([]ai.ToolCall{mk("f", "bash", `{"command":"git status"}`)}); r[0] {
		t.Fatal("after a reset, the same call runs again (run restarts at 1)")
	}

	w1 := ag.markDoomLoops([]ai.ToolCall{mk("g", "wait", `{"cmd":"true"}`)})
	w2 := ag.markDoomLoops([]ai.ToolCall{mk("h", "wait", `{"cmd":"true"}`)})
	w3 := ag.markDoomLoops([]ai.ToolCall{mk("i", "wait", `{"cmd":"true"}`)})
	if w1[0] || w2[0] || w3[0] {
		t.Fatal("wait must be exempt from the doom-loop guard")
	}
}

func TestDoomLoopRefusalText(t *testing.T) {
	msg := doomLoopRefusal("bash")
	if !strings.Contains(msg, "bash") || !strings.Contains(msg, "refused") {
		t.Fatalf("refusal should name the tool and the refusal: %q", msg)
	}
}

func TestDoomLoopRefusalSkipsExecution(t *testing.T) {
	var ran atomic.Int32
	tool := tools.Tool{
		Def: ai.NewTool("noop", "noop", `{"type":"object","properties":{"x":{"type":"string"}}}`),
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			ran.Add(1)
			return "ran", nil
		},
	}
	ag := New(nil, "m", 100, "sys")
	ag.Tools = []tools.Tool{tool}
	mk := func(id string) ai.ToolCall {
		var tc ai.ToolCall
		tc.ID = id
		tc.Function.Name = "noop"
		tc.Function.Arguments = `{"x":"same"}`
		return tc
	}

	var ended atomic.Int32
	ev := Events{OnToolEnd: func(string, string, string) { ended.Add(1) }}
	ag.runTools(context.Background(), []ai.ToolCall{mk("1")}, ev)
	ag.runTools(context.Background(), []ai.ToolCall{mk("2")}, ev)
	res := ag.runTools(context.Background(), []ai.ToolCall{mk("3")}, ev)
	if ran.Load() != 2 {
		t.Fatalf("tool body should run twice, ran %d", ran.Load())
	}
	if !strings.Contains(res[0].Text, "refused") {
		t.Fatalf("3rd call should return the refusal, got %q", res[0].Text)
	}
	if ended.Load() != 3 {
		t.Fatalf("OnToolEnd should fire for the refused call too, got %d", ended.Load())
	}
}

func TestSubCompactionUsageReachesParentLedger(t *testing.T) {
	var summaryCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ai.Request
		json.NewDecoder(r.Body).Decode(&req)
		if !req.Stream {
			summaryCalls.Add(1)
			w.Write([]byte(`{"choices":[{"message":{"content":"folded"}}],"usage":{"prompt_tokens":100,"completion_tokens":10}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[],"usage":{"prompt_tokens":400000,"completion_tokens":5}}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	parent := New(ai.New(srv.URL, "k"), "parent-m", 100, "sys")
	sub := parent.newSub(SubModel{Client: parent.Client, Model: "sub-m", ContextLimit: 1_000_000})
	sub.CompactThreshold = 0.30
	for range 6 {
		sub.Messages = append(sub.Messages,
			ai.Message{Role: "user", Content: strings.Repeat("q", 2000)},
			ai.Message{Role: "assistant", Content: strings.Repeat("a", 2000)},
		)
	}
	if _, err := sub.Turn(context.Background(), "continue", Events{}); err != nil {
		t.Fatal(err)
	}
	if summaryCalls.Load() != 1 {
		t.Fatalf("sub should have compacted once, summary calls = %d", summaryCalls.Load())
	}
	if u := parent.Usage(); u.PromptTokens != 0 {
		t.Fatalf("parent's own usage must stay 0, got %+v", u)
	}
	got := parent.SubUsage()["sub-m @ "]
	if got.PromptTokens != 400_100 || got.CompletionTokens != 15 {
		t.Fatalf("ledger should hold the sub's stream (400000/5) + summary (100/10): %+v", got)
	}
	if own := sub.Usage(); own != got {
		t.Fatalf("sub's own usage %+v should equal what the parent ledgered %+v", own, got)
	}
}
