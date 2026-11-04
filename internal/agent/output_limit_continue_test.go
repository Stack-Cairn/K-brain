package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// lengthServer replies to request n with frames(n, req); each frame is one SSE data line.
func lengthServer(t *testing.T, frames func(n int, req ai.Request) []string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ai.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		n := int(calls.Add(1))
		w.Header().Set("Content-Type", "text/event-stream")
		for _, frame := range frames(n, req) {
			fmt.Fprintf(w, "data: %s\n\n", frame)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

const (
	truncatedTextFrame = `{"choices":[{"delta":{"content":"partial"},"finish_reason":"length"}]}`
	finishedTextFrame  = `{"choices":[{"delta":{"content":"rest"},"finish_reason":"stop"}]}`
)

func lastUserMessage(req ai.Request) ai.Message {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			return req.Messages[i]
		}
	}
	return ai.Message{}
}

func TestOutputLimitContinuesReply(t *testing.T) {
	srv, calls := lengthServer(t, func(n int, req ai.Request) []string {
		if n == 1 {
			return []string{truncatedTextFrame}
		}
		if got := req.Messages[len(req.Messages)-2]; got.Role != "assistant" || !strings.HasPrefix(got.Content, "partial\n[response truncated by output limit]") {
			t.Errorf("continuation lost the partial reply: %+v", got)
		}
		if got := lastUserMessage(req).Content; !strings.Contains(got, "<"+ReminderTag+">") || !strings.Contains(got, "Continue exactly where it stopped") {
			t.Errorf("continuation reminder: %q", got)
		}
		return []string{finishedTextFrame}
	})
	ag := New(ai.New(srv.URL, "k"), "m", 100, "sys", WithOutputLimitContinuation())
	var attempts []int
	var streamed strings.Builder
	final, err := ag.Turn(t.Context(), "write a lot", Events{
		OnText:                func(d string) { streamed.WriteString(d) },
		OnOutputLimitContinue: func(n int) { attempts = append(attempts, n) },
	})
	if err != nil || final != "rest" || calls.Load() != 2 {
		t.Fatalf("final=%q err=%v calls=%d", final, err, calls.Load())
	}
	if !reflect.DeepEqual(attempts, []int{1}) || ag.LastStopReason() != ai.StopReasonStop {
		t.Fatalf("attempts=%v stop=%q", attempts, ag.LastStopReason())
	}
	if want := "partial\n[response truncated by output limit]\n\nrest"; streamed.String() != want {
		t.Fatalf("streamed %q, want %q", streamed.String(), want)
	}
	var roles []string
	for _, m := range ag.MessagesSnapshot() {
		roles = append(roles, m.Role+":"+string(m.StopReason))
	}
	if want := []string{"system:", "user:", "assistant:length", "user:", "assistant:stop"}; !reflect.DeepEqual(roles, want) {
		t.Fatalf("history %v, want %v", roles, want)
	}
}

func TestOutputLimitNeverRunsTruncatedToolCall(t *testing.T) {
	var ran []string
	srv, calls := lengthServer(t, func(n int, req ai.Request) []string {
		switch n {
		case 1:
			return []string{
				`{"choices":[{"delta":{"content":"writing"}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"big","type":"function","function":{"name":"echo","arguments":"{\"s\":\"half"}}]},"finish_reason":"length"}]}`,
			}
		case 2:
			if got := lastUserMessage(req).Content; !strings.Contains(got, "NOT executed") || !strings.Contains(got, "smaller pieces") {
				t.Errorf("tool-call reminder: %q", got)
			}
			for _, m := range req.Messages {
				if len(m.ToolCalls) > 0 || m.Role == "tool" {
					t.Errorf("discarded tool call reached history: %+v", m)
				}
			}
			return []string{`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"small","type":"function","function":{"name":"echo","arguments":"{\"s\":\"piece\"}"}}]},"finish_reason":"tool_calls"}]}`}
		default:
			return []string{finishedTextFrame}
		}
	})
	ag := New(ai.New(srv.URL, "k"), "m", 100, "sys", WithOutputLimitContinuation())
	echo := echoTool()
	run := echo.Run
	echo.Run = func(ctx context.Context, args json.RawMessage) (string, error) {
		ran = append(ran, string(args))
		return run(ctx, args)
	}
	ag.Tools = []tools.Tool{echo}
	var started []string
	final, err := ag.Turn(t.Context(), "write a big file", Events{OnToolStart: func(id, _, _ string) { started = append(started, id) }})
	if err != nil || final != "rest" || calls.Load() != 3 {
		t.Fatalf("final=%q err=%v calls=%d", final, err, calls.Load())
	}
	if !reflect.DeepEqual(ran, []string{`{"s":"piece"}`}) || !reflect.DeepEqual(started, []string{"small"}) {
		t.Fatalf("tool runs=%v starts=%v", ran, started)
	}
}

func TestOutputLimitContinuationCap(t *testing.T) {
	for _, tc := range []struct {
		name     string
		enabled  bool
		maxTurns int
		requests int32
	}{
		{"disabled", false, 0, 1},
		{"cap", true, 0, 1 + MaxOutputLimitContinuations},
		{"max turns", true, 2, 2},
		{"max turns reached", true, 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, calls := lengthServer(t, func(int, ai.Request) []string { return []string{truncatedTextFrame} })
			ag := New(ai.New(srv.URL, "k"), "m", 100, "sys")
			ag.ContinueOnOutputLimit = tc.enabled
			ag.MaxTurns = tc.maxTurns
			final, err := ag.Turn(t.Context(), "go", Events{})
			if err != nil || calls.Load() != tc.requests {
				t.Fatalf("err=%v requests=%d, want %d", err, calls.Load(), tc.requests)
			}
			if !strings.HasPrefix(final, "partial\n[response truncated") || ag.LastStopReason() != ai.StopReasonLength {
				t.Fatalf("final=%q stop=%q", final, ag.LastStopReason())
			}
			reminders := 0
			for _, m := range ag.MessagesSnapshot() {
				if m.Role == "user" && strings.Contains(m.Content, "output token limit") {
					reminders++
				}
			}
			if reminders != int(tc.requests)-1 {
				t.Fatalf("reminders=%d for %d requests", reminders, tc.requests)
			}
		})
	}
}

func TestOutputLimitContinuationStopsOnCancel(t *testing.T) {
	srv, calls := lengthServer(t, func(int, ai.Request) []string { return []string{truncatedTextFrame} })
	ag := New(ai.New(srv.URL, "k"), "m", 100, "sys", WithOutputLimitContinuation())
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	_, _ = ag.Turn(ctx, "go", Events{OnUsage: func(ai.Usage) { cancel() }})
	if calls.Load() != 1 {
		t.Fatalf("continued after cancel: requests=%d", calls.Load())
	}
	for _, m := range ag.MessagesSnapshot() {
		if strings.Contains(m.Content, "output token limit") {
			t.Fatal("reminder appended after cancel")
		}
	}
}

func TestSubagentInheritsOutputLimitContinuation(t *testing.T) {
	ag := New(ai.New("http://127.0.0.1:1", "k"), "m", 100, "sys", WithOutputLimitContinuation())
	if sub := ag.newSub(SubModel{Client: ag.Client, Model: "m"}); !sub.ContinueOnOutputLimit {
		t.Fatal("subagent dropped output-limit continuation")
	}
}
