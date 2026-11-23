package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// badArgsServer answers each request with a tool call whose arguments are
// cut off mid-string while bad > 0, then with a valid call, then text.
func badArgsServer(t *testing.T, bad int, seen func(req ai.Request)) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ai.Request
		json.NewDecoder(r.Body).Decode(&req)
		if seen != nil {
			seen(req)
		}
		n := int(calls.Add(1))
		w.Header().Set("Content-Type", "text/event-stream")
		switch {
		case n <= bad:
			fmt.Fprintf(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"b%d","type":"function","function":{"name":"echo","arguments":"{\"s\": \"cut"}}]}}]}`+"\n\n", n)
			fmt.Fprint(w, `data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n")
		case n == bad+1:
			fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"ok","type":"function","function":{"name":"echo","arguments":"{\"s\":\"fixed\"}"}}]}}]}`+"\n\n")
		default:
			fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`+"\n\n")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// Malformed tool-call arguments used to be dropped, which ended the turn
// silently. Now the call is answered with an error the model sees, the UI
// gets a failed tool row, and the model's corrected call runs.
func TestMalformedToolArgsAreAnsweredWithAnError(t *testing.T) {
	var fedBack string
	srv, calls := badArgsServer(t, 1, func(req ai.Request) {
		if last := req.Messages[len(req.Messages)-1]; last.Role == "tool" && last.ToolCallID == "b1" {
			fedBack = last.Content
		}
	})
	ag := New(ai.New(srv.URL, "k"), "m", 100, "sys")
	ag.Tools = []tools.Tool{echoTool()}
	var ends []string
	final, err := ag.Turn(context.Background(), "go", Events{
		OnToolEnd: func(id, name, out string) { ends = append(ends, id+":"+out) },
	})
	if err != nil || final != "done" || calls.Load() != 3 {
		t.Fatalf("final %q err %v after %d requests", final, err, calls.Load())
	}
	if !strings.Contains(fedBack, "not valid JSON") || !strings.Contains(fedBack, `{"s": "cut`) {
		t.Fatalf("model was not told what went wrong: %q", fedBack)
	}
	if len(ends) != 2 || !strings.HasPrefix(ends[0], "b1:Error:") || ends[1] != "ok:echoed: fixed" {
		t.Fatalf("tool rows: %q", ends)
	}
}

// A model that keeps sending malformed arguments gets one chance to correct
// them; the second malformed round ends the turn with an error.
func TestRepeatedMalformedToolArgsStopTheTurn(t *testing.T) {
	srv, calls := badArgsServer(t, 100, nil)
	ag := New(ai.New(srv.URL, "k"), "m", 100, "sys")
	ag.Tools = []tools.Tool{echoTool()}
	_, err := ag.Turn(context.Background(), "go", Events{})
	if !errors.Is(err, ErrMalformedToolCalls) || calls.Load() != 2 {
		t.Fatalf("err %v after %d requests", err, calls.Load())
	}
}
