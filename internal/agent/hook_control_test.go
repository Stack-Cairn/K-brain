package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/hooks"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// recordingServer answers each request with reply(n) for the n-th request
// (1-based) and keeps every request body.
type recordingServer struct {
	mu     sync.Mutex
	bodies []string
}

func (s *recordingServer) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.bodies...)
}

func newRecordingServer(t *testing.T, reply func(n int) string) (*recordingServer, *httptest.Server) {
	t.Helper()
	rec := &recordingServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec.mu.Lock()
		rec.bodies = append(rec.bodies, string(body))
		n := len(rec.bodies)
		rec.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, reply(n))
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return rec, srv
}

func textReply(text string) string {
	return fmt.Sprintf(`data: {"choices":[{"delta":{"content":%q},"finish_reason":"stop"}]}`+"\n\n", text)
}

func skipNonPOSIX(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell hook")
	}
}

func TestStopHookBlockRunsAnotherRound(t *testing.T) {
	skipNonPOSIX(t)
	rec, srv := newRecordingServer(t, func(n int) string { return textReply(fmt.Sprintf("answer %d", n)) })
	dir := t.TempDir()
	// Blocks the first stop only, and records every Stop payload it reads on stdin.
	script := `cat >> stops.jsonl; echo >> stops.jsonl; if [ ! -f blocked ]; then touch blocked; printf '{"decision":"block","reason":"run the tests first"}'; fi`
	ag := New(ai.New(srv.URL, "k"), "m", 100, "sys")
	ag.WorkingDir = dir
	ag.Hooks = hooks.New(map[string][]config.Hook{hooks.Stop: {{Command: script, Shell: "sh"}}})
	var streamed strings.Builder
	answer, err := ag.Turn(t.Context(), "go", Events{OnText: func(s string) { streamed.WriteString(s) }})
	if err != nil {
		t.Fatal(err)
	}
	bodies := rec.requests()
	if len(bodies) != 2 || answer != "answer 2" {
		t.Fatalf("requests=%d answer=%q", len(bodies), answer)
	}
	if strings.Contains(bodies[0], "run the tests first") || !strings.Contains(bodies[1], "run the tests first") {
		t.Fatal("block reason must reach only the continued request")
	}
	if streamed.String() != "answer 1\n\nanswer 2" {
		t.Fatalf("streamed %q", streamed.String())
	}
	data, err := os.ReadFile(filepath.Join(dir, "stops.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var events []hooks.Event
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var e hooks.Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("stop payload %q: %v", line, err)
		}
		events = append(events, e)
	}
	// One Stop per completion attempt; none again on exit.
	if len(events) != 2 || events[0].StopHookActive || !events[1].StopHookActive {
		t.Fatalf("stop events %+v", events)
	}
	var reminder ai.Message
	for _, m := range ag.MessagesSnapshot() {
		if m.Role == "user" && strings.Contains(m.Content, "run the tests first") {
			reminder = m
		}
	}
	if reminder.Origin != ai.OriginBackground || reminder.Authored {
		t.Fatalf("block reason message %+v", reminder)
	}
}

func TestStopHookBlockCap(t *testing.T) {
	skipNonPOSIX(t)
	rec, srv := newRecordingServer(t, func(n int) string { return textReply("again") })
	dir := t.TempDir()
	ag := New(ai.New(srv.URL, "k"), "m", 100, "sys")
	ag.WorkingDir = dir
	ag.Hooks = hooks.New(map[string][]config.Hook{hooks.Stop: {{Command: `echo x >> stops; printf '{"decision":"block","reason":"never satisfied"}'`, Shell: "sh"}}})
	if _, err := ag.Turn(t.Context(), "go", Events{}); err != nil {
		t.Fatal(err)
	}
	if got := len(rec.requests()); got != 1+MaxStopHookBlocks {
		t.Fatalf("requests = %d, want %d", got, 1+MaxStopHookBlocks)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "stops"))
	if got := strings.Count(string(data), "x"); got != 1+MaxStopHookBlocks {
		t.Fatalf("Stop ran %d times, want %d", got, 1+MaxStopHookBlocks)
	}
	// The next turn starts with a fresh count.
	if _, err := ag.Turn(t.Context(), "again", Events{}); err != nil {
		t.Fatal(err)
	}
	if got := len(rec.requests()); got != 2*(1+MaxStopHookBlocks) {
		t.Fatalf("requests after second turn = %d", got)
	}
}

func TestFailingStopHookDoesNotContinue(t *testing.T) {
	skipNonPOSIX(t)
	rec, srv := newRecordingServer(t, func(int) string { return textReply("done") })
	ag := New(ai.New(srv.URL, "k"), "m", 100, "sys")
	ag.WorkingDir = t.TempDir()
	ag.Hooks = hooks.New(map[string][]config.Hook{hooks.Stop: {{Command: `printf '{"decision":"block","reason":"x"}'; exit 1`, Shell: "sh"}}})
	if _, err := ag.Turn(t.Context(), "go", Events{}); err != nil {
		t.Fatal(err)
	}
	if got := len(rec.requests()); got != 1 {
		t.Fatalf("requests = %d", got)
	}
}

func TestPostToolUseAdditionalContextReachesModel(t *testing.T) {
	skipNonPOSIX(t)
	rec, srv := newRecordingServer(t, func(n int) string {
		if n == 1 {
			return `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"echo","arguments":"{\"s\":\"hi\"}"}}]},"finish_reason":"tool_calls"}]}` + "\n\n"
		}
		return textReply("done")
	})
	ag := New(ai.New(srv.URL, "k"), "m", 100, "sys")
	ag.WorkingDir = t.TempDir()
	ag.Tools = []tools.Tool{echoTool()}
	ag.Hooks = hooks.New(map[string][]config.Hook{hooks.PostToolUse: {
		{Command: `printf '{"additionalContext":"echo output is cached"}'`, Shell: "sh", Matcher: "echo"},
		{Command: `printf '{"additionalContext":"bash only"}'`, Shell: "sh", Matcher: "bash"},
	}})
	if _, err := ag.Turn(t.Context(), "go", Events{}); err != nil {
		t.Fatal(err)
	}
	bodies := rec.requests()
	if len(bodies) != 2 {
		t.Fatalf("requests = %d", len(bodies))
	}
	if !strings.Contains(bodies[1], "echo output is cached") || strings.Contains(bodies[1], "bash only") {
		t.Fatalf("second request lacks the hook context or has the unmatched one: %s", bodies[1])
	}
	var result string
	for _, m := range ag.MessagesSnapshot() {
		if m.Role == "tool" {
			result = m.Content
		}
	}
	if !strings.HasPrefix(result, "echoed: hi\n\n") || !strings.Contains(result, WrapReminder("Hook context:\necho output is cached")) {
		t.Fatalf("tool result %q", result)
	}
}

func TestUserPromptSubmitAdditionalContextReachesModel(t *testing.T) {
	skipNonPOSIX(t)
	rec, srv := newRecordingServer(t, func(int) string { return textReply("done") })
	ag := New(ai.New(srv.URL, "k"), "m", 100, "sys")
	ag.WorkingDir = t.TempDir()
	ag.Hooks = hooks.New(map[string][]config.Hook{hooks.UserPromptSubmit: {{Command: `printf '{"additionalContext":"current branch: feature-x"}'`, Shell: "sh"}}})
	if _, err := ag.Turn(t.Context(), "what branch?", Events{}); err != nil {
		t.Fatal(err)
	}
	bodies := rec.requests()
	if len(bodies) != 1 || !strings.Contains(bodies[0], "current branch: feature-x") {
		t.Fatalf("request lacks prompt hook context: %v", bodies)
	}
	for _, m := range ag.MessagesSnapshot() {
		if m.Role == "user" && m.Content != "what branch?" {
			t.Fatalf("visible user message changed: %q", m.Content)
		}
	}
}
