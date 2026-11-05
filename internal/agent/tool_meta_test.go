package agent

import (
	"context"
	"encoding/json"
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

// A shell call's exit status and duration reach OnToolResult and the stored tool
// call, while the request that carries the result to the model stays as before.
func TestShellExitStatusReachesResultAndHistoryNotRequest(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(raw))
		mu.Unlock()
		var req ai.Request
		_ = json.Unmarshal(raw, &req)
		w.Header().Set("Content-Type", "text/event-stream")
		if len(req.Messages) > 0 && req.Messages[len(req.Messages)-1].Role == "tool" {
			fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`+"\n\n")
		} else {
			fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"t1","type":"function","function":{"name":"bash","arguments":"{\"command\":\"exit 3\"}"}}]}}]}`+"\n\n")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	ag := New(ai.New(srv.URL, "k"), "m", 100, "sys")
	ag.WorkingDir = t.TempDir()
	ag.Tools = tools.All()
	var got tools.Result
	ctx := tools.WithWorkingDir(context.Background(), ag.WorkingDir)
	if _, err := ag.Turn(ctx, "go", Events{OnToolResult: func(_, _ string, r tools.Result) { got = r }}); err != nil {
		t.Fatal(err)
	}
	if got.ExitCode == nil || *got.ExitCode != 3 || got.Duration <= 0 {
		t.Fatalf("OnToolResult: %+v", got)
	}
	var call ai.ToolCall
	for _, m := range ag.Messages {
		if len(m.ToolCalls) > 0 {
			call = m.ToolCalls[0]
		}
	}
	if call.ExitStatus == nil || *call.ExitStatus != 3 || call.ExitCode != 3 || call.DurationMs < 1 || call.TimedOut {
		t.Fatalf("stored call: %+v", call)
	}
	mu.Lock()
	defer mu.Unlock()
	last := bodies[len(bodies)-1]
	for _, leak := range []string{"exit_status", "timed_out", "duration_ms"} {
		if strings.Contains(last, leak) {
			t.Errorf("%s leaked into the request: %s", leak, last)
		}
	}
	if !strings.Contains(last, `(exit: exit status 3)`) {
		t.Errorf("tool result text changed: %s", last)
	}
}
