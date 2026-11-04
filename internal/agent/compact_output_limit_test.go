package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// outputLimitServer is a Responses API endpoint whose summary replies come back
// "incomplete" (reason max_output_tokens) whenever reply says so, the way a
// reasoning model reports a budget spent before or while writing.
type outputLimitServer struct {
	// reply returns the text and whether the response completed, given the
	// requested max_output_tokens and whether the request carried tools.
	reply func(budget int, withTools bool) (text string, complete bool)

	mu      sync.Mutex
	budgets []int
	tools   []bool
}

func (s *outputLimitServer) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		var body struct {
			MaxOutputTokens int               `json:"max_output_tokens"`
			Tools           []json.RawMessage `json:"tools"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Error(err)
			return
		}
		withTools := len(body.Tools) > 0
		s.mu.Lock()
		s.budgets = append(s.budgets, body.MaxOutputTokens)
		s.tools = append(s.tools, withTools)
		s.mu.Unlock()
		text, complete := s.reply(body.MaxOutputTokens, withTools)
		status, details := "completed", `null`
		if !complete {
			status, details = "incomplete", `{"reason":"max_output_tokens"}`
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"status":%q,"incomplete_details":%s,"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":%q}]}]}`, status, details, text)
	}
}

func outputLimitAgent(t *testing.T, srv *outputLimitServer, maxTokens int) *Agent {
	t.Helper()
	server := httptest.NewServer(srv.handler(t))
	t.Cleanup(server.Close)
	a := New(ai.NewResponses(server.URL, "fixture"), "fixture", maxTokens, "stable")
	a.Tools = []tools.Tool{echoTool()}
	a.ContextLimit = 1_000_000
	for i := range 4 {
		a.Messages = append(a.Messages,
			ai.Message{Role: "user", Content: fmt.Sprintf("turn-%d ", i) + strings.Repeat("历史 ", 200)},
			ai.Message{Role: "assistant", Content: "answer"})
	}
	return a
}

// The user-reported failure: "compaction summary failed: response truncated by
// output limit: incomplete.max_output_tokens". Reasoning spent the 4096 budget
// before any summary text was written; a larger budget completes it.
func TestCompactionRetriesSummaryCutOffByOutputLimit(t *testing.T) {
	srv := &outputLimitServer{reply: func(budget int, _ bool) (string, bool) {
		if budget < 16_384 {
			return "", false
		}
		return "full summary", true
	}}
	a := outputLimitAgent(t, srv, 64_000)

	summary, _, _, err := a.compact(t.Context())
	if err != nil {
		t.Fatalf("compaction failed instead of retrying with a larger budget: %v", err)
	}
	if summary != "full summary" {
		t.Fatalf("summary = %q", summary)
	}
	if want := []int{4096, 16_384}; fmt.Sprint(srv.budgets) != fmt.Sprint(want) {
		t.Fatalf("budgets = %v, want %v (start as before, then grow)", srv.budgets, want)
	}
	if !srv.tools[0] || !srv.tools[1] {
		t.Fatal("the retry should stay on the cached same-model prompt")
	}
}

// The budget never suffices, on either prompt: the growth stops at the model's
// catalog output limit, and a cut-off summary is never used, so compaction
// fails with the output-limit error and history stays as it was.
func TestCompactionStillTruncatedPreservesHistory(t *testing.T) {
	srv := &outputLimitServer{reply: func(budget int, _ bool) (string, bool) {
		return fmt.Sprintf("Objective: partial-%d", budget), false
	}}
	a := outputLimitAgent(t, srv, 8192)
	before := a.MessagesSnapshot()

	_, _, _, err := a.compact(t.Context())
	if _, ok := errors.AsType[*ai.OutputLimitError](err); !ok {
		t.Fatalf("err = %v, want the output-limit error", err)
	}
	if want := []int{4096, 8192, 4096, 8192}; fmt.Sprint(srv.budgets) != fmt.Sprint(want) {
		t.Fatalf("budgets = %v, want %v (cached prompt, then transcript, each capped at MaxTokens)", srv.budgets, want)
	}
	if fmt.Sprint(before) != fmt.Sprint(a.MessagesSnapshot()) {
		t.Fatal("a truncated summary must not replace history")
	}
}

// The cached prompt (with tools and reasoning effort) never yields any text:
// compaction falls back to the plain transcript prompt rather than failing.
func TestCompactionFallsBackWhenCachedSummaryHasNoText(t *testing.T) {
	srv := &outputLimitServer{reply: func(_ int, withTools bool) (string, bool) {
		if withTools {
			return "", false
		}
		return "transcript summary", true
	}}
	a := outputLimitAgent(t, srv, 4096)

	summary, _, _, err := a.compact(t.Context())
	if err != nil {
		t.Fatalf("compaction failed instead of falling back: %v", err)
	}
	if summary != "transcript summary" {
		t.Fatalf("summary = %q", summary)
	}
	if last := srv.tools[len(srv.tools)-1]; last {
		t.Fatal("the fallback must use the plain transcript prompt without tools")
	}
}

// A model whose output limit is at or below the starting budget gets no retry
// it cannot serve: the output-limit error surfaces after one request.
func TestCompleteSummaryDoesNotGrowPastModelLimit(t *testing.T) {
	srv := &outputLimitServer{reply: func(int, bool) (string, bool) { return "", false }}
	a := outputLimitAgent(t, srv, 2048)

	_, _, err := a.completeSummary(t.Context(), a.Client, ai.Request{Model: "fixture",
		Messages: []ai.Message{{Role: "user", Content: "summarize"}}}, a.summaryOutputCeiling(false), "fixture")
	if _, ok := errors.AsType[*ai.OutputLimitError](err); !ok {
		t.Fatalf("err = %v, want the output-limit error", err)
	}
	if want := []int{4096}; fmt.Sprint(srv.budgets) != fmt.Sprint(want) {
		t.Fatalf("budgets = %v, want %v", srv.budgets, want)
	}
}
