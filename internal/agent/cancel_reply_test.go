package agent

import (
	"context"
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

// cancelServer answers Chat Completions requests from a script. A round with
// block set streams its events and then holds the response open until the client
// goes away, the way a model still generating looks when the user presses stop.
type cancelServer struct {
	mu       sync.Mutex
	rounds   []cancelRound
	requests []json.RawMessage
}

type cancelRound struct {
	events []string
	block  bool
}

func (s *cancelServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.requests = append(s.requests, body)
	round := cancelRound{events: []string{`{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`}}
	if len(s.rounds) > 0 {
		round, s.rounds = s.rounds[0], s.rounds[1:]
	}
	s.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	for _, event := range round.events {
		fmt.Fprintf(w, "data: %s\n\n", event)
	}
	w.(http.Flusher).Flush()
	if round.block {
		<-r.Context().Done()
		return
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func (s *cancelServer) request(t *testing.T, i int) []map[string]any {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if i >= len(s.requests) {
		t.Fatalf("request %d was not sent (%d requests)", i, len(s.requests))
	}
	var body struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(s.requests[i], &body); err != nil {
		t.Fatal(err)
	}
	return body.Messages
}

func newCancelAgent(t *testing.T, rounds ...cancelRound) (*Agent, *cancelServer) {
	t.Helper()
	fixture := &cancelServer{rounds: rounds}
	srv := httptest.NewServer(fixture)
	t.Cleanup(srv.Close)
	return New(ai.New(srv.URL, "key"), "model", 100, "system"), fixture
}

// stopOnText cancels the turn once the given text has streamed to the user.
func stopOnText(cancel context.CancelFunc, want string) Events {
	var seen strings.Builder
	return Events{OnText: func(d string) {
		seen.WriteString(d)
		if strings.Contains(seen.String(), want) {
			cancel()
		}
	}}
}

func TestCancelMidStreamKeepsStreamedReply(t *testing.T) {
	ag, fixture := newCancelAgent(t, cancelRound{block: true, events: []string{
		`{"choices":[{"delta":{"reasoning_content":"weighing it"}}]}`,
		`{"choices":[{"delta":{"content":"Hello, par"}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call","function":{"name":"write","arguments":"{\"pa"}}]}}]}`,
	}})
	var runs int
	ag.Tools = []tools.Tool{{Def: ai.NewTool("write", "write", `{"type":"object"}`), Run: func(context.Context, json.RawMessage) (string, error) { runs++; return "ok", nil }}}
	ctx, cancel := context.WithCancel(t.Context())
	_, err := ag.Turn(ctx, "greet me", stopOnText(cancel, "par"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	msgs := ag.MessagesSnapshot()
	if len(msgs) != 3 || msgs[1].Role != "user" {
		t.Fatalf("history = %+v", msgs)
	}
	reply := msgs[2]
	if reply.Role != "assistant" || reply.StopReason != ai.StopReasonCancelled || reply.Content != "Hello, par" || reply.Reasoning != "weighing it" {
		t.Fatalf("cancelled reply = %+v", reply)
	}
	if len(reply.ToolCalls) != 0 || runs != 0 {
		t.Fatalf("unfinished tool call kept or run: calls=%+v runs=%d", reply.ToolCalls, runs)
	}
	if reply.ID == "" || reply.SentAt == nil || reply.Model != "model @ " {
		t.Fatalf("cancelled reply lacks metadata: %+v", reply)
	}

	if _, err := ag.Turn(t.Context(), "go on", Events{}); err != nil {
		t.Fatal(err)
	}
	next := fixture.request(t, 1)
	if len(next) != 4 || next[2]["role"] != "assistant" || next[2]["content"] != "Hello, par\n\n[response interrupted by the user]" || next[2]["tool_calls"] != nil {
		t.Fatalf("next request = %+v", next)
	}
	if next[3]["role"] != "user" || next[3]["content"] != "go on" {
		t.Fatalf("next request tail = %+v", next[3])
	}
}

func TestCancelAfterThinkingOnlyKeepsItOutOfRequests(t *testing.T) {
	ag, fixture := newCancelAgent(t, cancelRound{block: true, events: []string{
		`{"choices":[{"delta":{"reasoning_content":"still thinking"}}]}`,
	}})
	ctx, cancel := context.WithCancel(t.Context())
	_, err := ag.Turn(ctx, "hard question", Events{OnThink: func(string) { cancel() }})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	msgs := ag.MessagesSnapshot()
	if len(msgs) != 3 || msgs[2].StopReason != ai.StopReasonCancelled || msgs[2].Content != "" || msgs[2].Reasoning != "still thinking" {
		t.Fatalf("history = %+v", msgs)
	}
	if _, err := ag.Turn(t.Context(), "again", Events{}); err != nil {
		t.Fatal(err)
	}
	next := fixture.request(t, 1)
	if len(next) != 3 || next[1]["content"] != "hard question" || next[2]["content"] != "again" {
		t.Fatalf("thinking-only reply reached the provider: %+v", next)
	}
}

func TestCancelBeforeOutputAddsNoReply(t *testing.T) {
	ag, _ := newCancelAgent(t, cancelRound{block: true})
	ctx, cancel := context.WithCancel(t.Context())
	_, err := ag.Turn(ctx, "hello", Events{OnPhase: func(phase string) {
		if phase == "waiting_model" {
			cancel()
		}
	}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	msgs := ag.MessagesSnapshot()
	if len(msgs) != 2 || msgs[1].Role != "user" {
		t.Fatalf("history = %+v", msgs)
	}
}

func TestFailedStreamDoesNotKeepPartialReply(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"half\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"error\":{\"message\":\"upstream failed\"}}\n\n")
	}))
	defer srv.Close()
	ag := New(ai.New(srv.URL, "key"), "model", 100, "system")
	if _, err := ag.Turn(t.Context(), "hello", Events{}); err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	for _, msg := range ag.MessagesSnapshot() {
		if msg.Role == "assistant" {
			t.Fatalf("failed stream entered history: %+v", msg)
		}
	}
}

func TestCancelDuringToolsLeavesValidHistory(t *testing.T) {
	ag, fixture := newCancelAgent(t, cancelRound{events: []string{
		`{"choices":[{"delta":{"content":"Checking."}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-done","function":{"name":"quick","arguments":"{}"}},{"index":1,"id":"call-slow","function":{"name":"slow","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
	}})
	ctx, cancel := context.WithCancel(t.Context())
	quickDone := make(chan struct{})
	ag.Tools = []tools.Tool{
		{Def: ai.NewTool("quick", "quick", `{"type":"object"}`), Run: func(context.Context, json.RawMessage) (string, error) {
			defer close(quickDone)
			return "quick result", nil
		}},
		{Def: ai.NewTool("slow", "slow", `{"type":"object"}`), Run: func(ctx context.Context, _ json.RawMessage) (string, error) {
			<-quickDone
			cancel()
			<-ctx.Done()
			return "", ctx.Err()
		}},
	}
	_, err := ag.Turn(ctx, "inspect", Events{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	msgs := ag.MessagesSnapshot()
	if len(msgs) != 5 || msgs[2].Role != "assistant" || len(msgs[2].ToolCalls) != 2 || msgs[2].Content != "Checking." {
		t.Fatalf("history = %+v", msgs)
	}
	results := map[string]ai.Message{msgs[3].ToolCallID: msgs[3], msgs[4].ToolCallID: msgs[4]}
	if done := results["call-done"]; done.Role != "tool" || done.Content != "quick result" || done.StopReason != "" {
		t.Fatalf("completed tool result = %+v", done)
	}
	if slow := results["call-slow"]; slow.Role != "tool" || slow.StopReason != ai.StopReasonCancelled {
		t.Fatalf("cancelled tool result = %+v", slow)
	}

	if _, err := ag.Turn(t.Context(), "continue", Events{}); err != nil {
		t.Fatalf("next run rejected history: %v", err)
	}
	next := fixture.request(t, 1)
	if len(next) != 6 || next[2]["role"] != "assistant" || next[3]["role"] != "tool" || next[4]["role"] != "tool" || next[5]["content"] != "continue" {
		t.Fatalf("next request = %+v", next)
	}
	answered := map[any]bool{next[3]["tool_call_id"]: true, next[4]["tool_call_id"]: true}
	if !answered["call-done"] || !answered["call-slow"] {
		t.Fatalf("tool calls left unanswered: %+v", next)
	}
}
