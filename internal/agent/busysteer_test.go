package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func TestInFlightToolsTracking(t *testing.T) {
	ag := New(ai.New("http://unused", "k"), "m", 100, "sys")
	ag.trackTool("subagent", 1)
	ag.trackTool("read", 1)
	ag.trackTool("bash", 1)
	if ag.subagentInflight.Load() != 1 || ag.otherInflight.Load() != 2 {
		t.Fatalf("counts = %d subagent / %d other, want 1/2", ag.subagentInflight.Load(), ag.otherInflight.Load())
	}
	ag.trackTool("read", -1)
	ag.trackTool("bash", -1)
	if ag.otherInflight.Load() != 0 {
		t.Fatalf("other should drain, got %d", ag.otherInflight.Load())
	}
	ag.trackTool("subagent", -1)
	if ag.subagentInflight.Load() != 0 {
		t.Fatalf("subagent should drain, got %d", ag.subagentInflight.Load())
	}
}

func TestWaitingOnSubagentsGating(t *testing.T) {
	ag := New(ai.New("http://unused", "k"), "m", 100, "sys")

	if ag.WaitingOnSubagents() {
		t.Fatal("no turn running → not waiting")
	}
	ag.running.Store(true)
	if ag.WaitingOnSubagents() {
		t.Fatal("turn running but nothing in flight → mid-generation, not waiting")
	}
	ag.trackTool("subagent", 1)
	if !ag.WaitingOnSubagents() {
		t.Fatal("only a subagent in flight → waiting")
	}
	ag.trackTool("subagent", 1)
	if !ag.WaitingOnSubagents() {
		t.Fatal("multiple subagents in flight → waiting")
	}
	ag.trackTool("bash", 1)
	if ag.WaitingOnSubagents() {
		t.Fatal("a bash in flight → not waiting on subagents")
	}
	ag.trackTool("bash", -1)
	ag.trackTool("subagent", -2)
	if ag.WaitingOnSubagents() {
		t.Fatal("all tools finished → not waiting")
	}
}

func TestWaitingOnSubagentsDuringForegroundSubagent(t *testing.T) {
	release := make(chan struct{})
	subStarted := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ai.Request
		json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "text/event-stream")
		switch {
		case len(req.Messages) > 0 && strings.HasPrefix(req.Messages[0].Content, "You are a subagent"):
			close(subStarted)
			<-release
			fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"sub report"},"finish_reason":"stop"}]}`+"\n\n")
		case len(req.Messages) > 0 && req.Messages[len(req.Messages)-1].Role == "user" && req.Messages[len(req.Messages)-1].Content == "go":
			fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"t1","type":"function","function":{"name":"subagent","arguments":"{\"prompt\":\"explore\"}"}}]}}]}`+"\n\n")
			fmt.Fprint(w, `data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n")
		default:
			fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"parent done"},"finish_reason":"stop"}]}`+"\n\n")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	ag := New(ai.New(srv.URL, "k"), "m", 100, "sys")
	done := make(chan error, 1)
	go func() {
		_, err := ag.Turn(t.Context(), "go", Events{})
		done <- err
	}()

	select {
	case <-subStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("subagent never started")
	}
	if !ag.WaitingOnSubagents() {
		t.Fatal("parent blocked on a foreground subagent must report WaitingOnSubagents")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if ag.WaitingOnSubagents() {
		t.Fatal("turn finished → no longer waiting")
	}
}

func TestDrainOrphanedSteers(t *testing.T) {
	ag := New(ai.New("http://unused", "k"), "m", 100, "sys")

	ag.Steer("keep me")
	ag.drainOrphanedSteers()
	if got := ag.drainPending(); len(got) != 1 || got[0].text != "keep me" {
		t.Fatalf("no hook: pending should survive, got %+v", got)
	}

	var surfaced []string
	ag.OnOrphanedSteer = func(text string) { surfaced = append(surfaced, text) }
	ag.running.Store(true)
	ag.Steer("one")
	ag.Steer("two")
	ag.running.Store(false)
	ag.drainOrphanedSteers()
	if len(surfaced) != 2 || surfaced[0] != "one" || surfaced[1] != "two" {
		t.Fatalf("hook should receive both steers in order, got %v", surfaced)
	}
	if got := ag.drainPending(); len(got) != 0 {
		t.Fatalf("orphaned steers must be drained, got %+v", got)
	}
}

func TestSteerOnIdleAgentFiresOrphanHook(t *testing.T) {
	ag := New(ai.New("http://unused", "k"), "m", 100, "sys")

	ag.Steer("parked")
	if got := ag.drainPending(); len(got) != 1 {
		t.Fatalf("no hook: steer should park, got %+v", got)
	}

	var fired []string
	ag.OnOrphanedSteer = func(text string) { fired = append(fired, text) }
	ag.Steer("late arrival")
	if len(fired) != 1 || fired[0] != "late arrival" {
		t.Fatalf("idle steer should fire the hook, got %v", fired)
	}
	if got := ag.drainPending(); len(got) != 0 {
		t.Fatalf("fired steer must not also park, got %+v", got)
	}
}
