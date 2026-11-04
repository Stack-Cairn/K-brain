package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/hooks"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

type hookRecorder struct {
	mu     sync.Mutex
	events []hooks.Event
}

func (r *hookRecorder) hook(_ context.Context, event hooks.Event) error {
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
	return nil
}

func (r *hookRecorder) named(name string) []hooks.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []hooks.Event
	for _, event := range r.events {
		if event.Name == name {
			out = append(out, event)
		}
	}
	return out
}

// toolRoundServer asks for the given tool calls once, then answers "done".
func toolRoundServer(t *testing.T, calls string) *httptest.Server {
	t.Helper()
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			fmt.Fprintf(w, `data: {"choices":[{"delta":{"tool_calls":%s},"finish_reason":"tool_calls"}]}`+"\n\n", calls)
		} else {
			fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`+"\n\n")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestPostToolUseFailureHook(t *testing.T) {
	srv := toolRoundServer(t, `[{"index":0,"id":"ok","type":"function","function":{"name":"echo","arguments":"{\"s\":\"hi\"}"}},{"index":1,"id":"bad","type":"function","function":{"name":"broken","arguments":"{}"}}]`)
	ag := New(ai.New(srv.URL, "k"), "m", 100, "sys")
	ag.Tools = []tools.Tool{echoTool(), {
		Def: ai.NewTool("broken", "fails", `{"type":"object"}`),
		Run: func(context.Context, json.RawMessage) (string, error) { return "", errors.New("disk full") },
	}}
	var rec hookRecorder
	ag.PluginHook = rec.hook
	if _, err := ag.Turn(t.Context(), "go", Events{}); err != nil {
		t.Fatal(err)
	}
	if post := rec.named(hooks.PostToolUse); len(post) != 2 {
		t.Fatalf("PostToolUse must still fire for every call: %+v", post)
	}
	failures := rec.named(hooks.PostToolUseFailure)
	if len(failures) != 1 || failures[0].ToolID != "bad" || failures[0].ToolName != "broken" || failures[0].ToolResult != "Error: disk full" || failures[0].ToolArgs != "{}" {
		t.Fatalf("PostToolUseFailure events: %+v", failures)
	}
}

func TestPreToolUseStructuredDenyReachesModel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell hook")
	}
	srv := toolRoundServer(t, `[{"index":0,"id":"c1","type":"function","function":{"name":"echo","arguments":"{\"s\":\"hi\"}"}}]`)
	ag := New(ai.New(srv.URL, "k"), "m", 100, "sys")
	ran := false
	echo := echoTool()
	echo.Run = func(context.Context, json.RawMessage) (string, error) { ran = true; return "", nil }
	ag.Tools = []tools.Tool{echo}
	ag.Hooks = hooks.New(map[string][]config.Hook{hooks.PreToolUse: {{Command: `printf '{"decision":"deny","reason":"echo is disabled here"}'`, Shell: "sh"}}})
	if _, err := ag.Turn(t.Context(), "go", Events{}); err != nil {
		t.Fatal(err)
	}
	if ran {
		t.Fatal("denied tool ran")
	}
	var result string
	for _, m := range ag.MessagesSnapshot() {
		if m.Role == "tool" {
			result = m.Content
		}
	}
	if result != "Error: hook PreToolUse denied tool call: echo is disabled here" {
		t.Fatalf("tool result = %q", result)
	}
}

func compactHistory(ag *Agent) {
	for i := range 8 {
		ag.Messages = append(ag.Messages,
			ai.Message{Role: "user", Content: fmt.Sprintf("q%d", i)},
			ai.Message{Role: "assistant", Content: fmt.Sprintf("a%d", i)},
		)
	}
}

func TestCompactHooksManual(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"content":"sim"}}]}`))
	}))
	defer srv.Close()
	for _, commit := range []bool{false, true} {
		ag := New(ai.New(srv.URL, "k"), "m", 100, "sys")
		ag.SetSessionID("s1")
		compactHistory(ag)
		var rec hookRecorder
		ag.PluginHook = rec.hook
		before := len(ag.Messages)
		var err error
		if commit {
			err = ag.ManualCompactCommit(t.Context(), Events{}, nil)
		} else {
			err = ag.ManualCompact(t.Context(), Events{})
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(rec.events) != 2 || rec.events[0].Name != hooks.PreCompact || rec.events[1].Name != hooks.PostCompact {
			t.Fatalf("compact hook order: %+v", rec.events)
		}
		pre, post := rec.events[0], rec.events[1]
		if pre.Trigger != "manual" || pre.MessagesBefore != before || pre.TokensBefore == 0 || pre.SessionID != "s1" || pre.Summary != "" || pre.MessagesAfter != 0 {
			t.Fatalf("PreCompact: %+v", pre)
		}
		if post.Trigger != "manual" || post.MessagesBefore != before || post.MessagesAfter != len(ag.Messages) || post.TokensAfter == 0 || post.Summary != "sim" {
			t.Fatalf("PostCompact: %+v", post)
		}
	}
}

func TestCompactHooksAutoAndFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ai.Request
		json.NewDecoder(r.Body).Decode(&req)
		if !req.Stream {
			w.Write([]byte(`{"choices":[{"message":{"content":"summary of prior work"}}]}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"ok"}}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	ag := New(ai.New(srv.URL, "k"), "m", 100, "sys")
	ag.ContextLimit = 1000
	for range 8 {
		ag.Messages = append(ag.Messages, ai.Message{Role: "user", Content: strings.Repeat("x", 120)})
	}
	var rec hookRecorder
	// A failing compaction hook is reported nowhere and never blocks compaction.
	ag.PluginHook = func(ctx context.Context, event hooks.Event) error {
		_ = rec.hook(ctx, event)
		if event.Name == hooks.PreCompact || event.Name == hooks.PostCompact {
			return errors.New("hook broke")
		}
		return nil
	}
	if _, err := ag.Turn(t.Context(), strings.Repeat("z", 900), Events{}); err != nil {
		t.Fatal(err)
	}
	pre, post := rec.named(hooks.PreCompact), rec.named(hooks.PostCompact)
	if len(pre) != 1 || len(post) != 1 || pre[0].Trigger != "auto" || post[0].Trigger != "auto" || post[0].Summary != "summary of prior work" {
		t.Fatalf("auto compact hooks: pre=%+v post=%+v", pre, post)
	}

	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"boom"}}`, http.StatusBadRequest)
	}))
	defer failing.Close()
	broken := New(ai.New(failing.URL, "k"), "m", 100, "sys")
	compactHistory(broken)
	var brokenRec hookRecorder
	broken.PluginHook = brokenRec.hook
	if err := broken.ManualCompact(t.Context(), Events{}); err == nil {
		t.Fatal("compaction against a failing provider succeeded")
	}
	if len(brokenRec.named(hooks.PreCompact)) != 1 || len(brokenRec.named(hooks.PostCompact)) != 0 {
		t.Fatalf("failed compaction hooks: %+v", brokenRec.events)
	}
	short := New(ai.New(failing.URL, "k"), "m", 100, "sys")
	var shortRec hookRecorder
	short.PluginHook = shortRec.hook
	_ = short.ManualCompact(t.Context(), Events{})
	if len(shortRec.events) != 0 {
		t.Fatalf("hooks fired with nothing to compact: %+v", shortRec.events)
	}
}
