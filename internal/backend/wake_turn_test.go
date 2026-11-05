package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

// handoffClient plays a turn that hands background work off with one tool call and
// then ends; later turns (the wake runs) answer with text. With block set, the
// second request of the first turn waits until the run is stopped.
type handoffClient struct {
	mu       sync.Mutex
	calls    int
	tool     string
	args     string
	block    bool
	requests [][]ai.Message
}

func (c *handoffClient) Models(context.Context) ([]ai.ModelInfo, error) { return nil, nil }
func (c *handoffClient) Complete(context.Context, ai.Request) (string, ai.Usage, error) {
	return "", ai.Usage{}, nil
}
func (c *handoffClient) Clone() ai.Client   { return c }
func (c *handoffClient) SetCacheKey(string) {}
func (c *handoffClient) Endpoint() string   { return "http://handoff-fixture.invalid" }
func (c *handoffClient) Stream(ctx context.Context, request ai.Request, onText, _ func(string), _ func(string, string, string)) (ai.Message, ai.Usage, error) {
	c.mu.Lock()
	c.calls++
	call, block := c.calls, c.block
	c.requests = append(c.requests, append([]ai.Message(nil), request.Messages...))
	c.mu.Unlock()
	switch {
	case call == 1:
		tc := ai.ToolCall{ID: "handoff", Type: "function"}
		tc.Function.Name, tc.Function.Arguments = c.tool, c.args
		return ai.Message{Role: "assistant", ToolCalls: []ai.ToolCall{tc}}, ai.Usage{}, nil
	case call == 2 && block:
		<-ctx.Done()
		return ai.Message{}, ai.Usage{}, ctx.Err()
	}
	text := "handed off; I will continue when notified"
	if call > 2 {
		text = "continuing the task"
	}
	if onText != nil {
		onText(text)
	}
	return ai.Message{Role: "assistant", Content: text, StopReason: ai.StopReasonStop}, ai.Usage{}, nil
}

func newHandoffServer(t *testing.T, client ai.Client) (string, *runtimeSession) {
	t.Helper()
	base, rt, _ := newHandoffServerWith(t, client)
	return base, rt
}

func newHandoffServerWith(t *testing.T, client ai.Client) (string, *runtimeSession, *Server) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell commands")
	}
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	factory := func(_ context.Context, _ string, selected protocol.ModelRef) (*agent.Agent, error) {
		ag := agent.New(client, selected.Model, 1024, "system")
		ag.ModelName, ag.Provider = selected.Model, selected.Provider
		return ag, nil
	}
	s, err := New(Options{Store: store, Factory: factory, EventDir: t.TempDir(), DefaultCWD: t.TempDir(), Models: []protocol.ModelRef{{Provider: "fixture", Model: "fixture-model"}}})
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(s)
	t.Cleanup(func() {
		hs.Close()
		_ = s.Close()
	})
	sess := createTestSession(t, hs.URL)
	rt, err := s.loadRuntimeByID(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.releaseRuntime(rt) })
	return hs.URL, rt, s
}

func startHandoffRun(t *testing.T, base string, rt *runtimeSession) protocol.RunAccepted {
	t.Helper()
	var accepted protocol.RunAccepted
	resp := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+rt.id+"/runs", protocol.PromptRequest{
		ConversationID: rt.id, ClientRequestID: "user-run", Prompt: "build, then continue once it is ready",
		Options: &protocol.RunOptions{ApprovalPolicy: "auto"},
	}, &accepted)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("run status %d", resp.StatusCode)
	}
	return accepted
}

func waitRunTerminal(t *testing.T, rt *runtimeSession, requestID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rt.mu.Lock()
		done := rt.runs[requestID].Terminal && rt.runDone
		rt.mu.Unlock()
		if done {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("run %s did not finish", requestID)
}

func jsonArgs(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A wait the model starts keeps polling after the turn that started it ends, and its
// result starts the next turn: the wait tool promises exactly that, so the model
// ends its turn instead of polling.
func TestWaitStartedByRunWakesSessionAfterRunEnds(t *testing.T) {
	flag := filepath.Join(t.TempDir(), "build.log")
	client := &handoffClient{tool: "wait"}
	client.args = jsonArgs(t, map[string]any{"command": "cat " + flag + " 2>/dev/null; true", "until": "BUILD-READY", "interval": 2, "timeout": 60})
	base, rt := newHandoffServer(t, client)
	startHandoffRun(t, base, rt)
	waitRunTerminal(t, rt, "user-run")
	if n := rt.agent.Waits().Active(); n != 1 {
		t.Fatalf("polling waits after the run ended = %d, want 1", n)
	}
	if err := os.WriteFile(flag, []byte("BUILD-READY done\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runs := waitForWakeRuns(t, rt, 1)
	text := userMessageTexts(t, readEventsAfter(t, base, rt.id, runs[0].AcceptedSeq-1), runs[0].RunID)
	if !strings.Contains(text, "condition met") || !strings.Contains(text, "BUILD-READY done") {
		t.Fatalf("wake user message = %q", text)
	}
	assertBackgroundNotice(t, base, rt, readEventsAfter(t, base, rt.id, runs[0].AcceptedSeq-1), runs[0].RunID)
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.calls != 3 {
		t.Fatalf("model calls = %d, want the wake run to reach the model", client.calls)
	}
}

// assertBackgroundNotice checks that the wake run's user message is marked as a
// background notice, live and in history, while the user's own prompt is not.
func assertBackgroundNotice(t *testing.T, base string, rt *runtimeSession, events []protocol.Event, wakeRunID string) {
	t.Helper()
	for _, e := range events {
		if e.Type != protocol.EventUserMessage || e.RunID != wakeRunID {
			continue
		}
		var msg protocol.Message
		if err := json.Unmarshal(e.Payload, &msg); err != nil {
			t.Fatal(err)
		}
		if msg.Origin != ai.OriginBackground {
			t.Fatalf("wake user_message origin = %q", msg.Origin)
		}
	}
	var page historyResponse
	if code := doJSON(t, http.MethodGet, base+"/v1/sessions/"+rt.id+"/history", nil, &page); code != http.StatusOK {
		t.Fatalf("history status=%d", code)
	}
	var notices, prompts int
	for _, m := range page.Session.Messages {
		if m.Role != protocol.RoleUser {
			continue
		}
		var text strings.Builder
		for _, block := range m.Content {
			text.WriteString(block.Text)
		}
		switch {
		case strings.Contains(text.String(), "build, then continue"):
			prompts++
			if m.Origin != "" {
				t.Fatalf("user prompt origin = %q", m.Origin)
			}
		case m.Origin == ai.OriginBackground:
			notices++
		default:
			t.Fatalf("unmarked user message in history: %q", text.String())
		}
	}
	if prompts != 1 || notices != 1 {
		t.Fatalf("history user messages: prompts=%d notices=%d", prompts, notices)
	}
	for _, m := range rt.agent.MessagesSnapshot() {
		if m.Origin == ai.OriginBackground && m.Authored {
			t.Fatal("a background notice must not count as authored by the user")
		}
	}
}

// Stopping the run still ends the waits it started.
func TestStoppingRunCancelsItsWait(t *testing.T) {
	flag := filepath.Join(t.TempDir(), "build.log")
	client := &handoffClient{tool: "wait", block: true}
	client.args = jsonArgs(t, map[string]any{"command": "cat " + flag + " 2>/dev/null; true", "until": "BUILD-READY", "interval": 2, "timeout": 60})
	base, rt := newHandoffServer(t, client)
	accepted := startHandoffRun(t, base, rt)
	deadline := time.Now().Add(5 * time.Second)
	for rt.agent.Waits().Active() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	resp := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+rt.id+"/runs/"+accepted.RunID+"/cancel", map[string]any{}, nil)
	_ = resp.Body.Close()
	waitRunTerminal(t, rt, "user-run")
	for rt.agent.Waits().Active() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := rt.agent.Waits().Active(); n != 0 {
		t.Fatalf("polling waits after the run was stopped = %d", n)
	}
}

// A managed process the model starts keeps running after the turn ends and reports
// its exit as the next turn.
func TestManagedProcessOutlivesRunAndReportsExit(t *testing.T) {
	client := &handoffClient{tool: "ManagedProcess"}
	client.args = jsonArgs(t, map[string]any{"action": "start", "command": "sleep 1; echo BUILD-READY", "label": "build"})
	base, rt := newHandoffServer(t, client)
	startHandoffRun(t, base, rt)
	waitRunTerminal(t, rt, "user-run")
	rt.mu.Lock()
	running := rt.processManager.HasRunningProcesses()
	rt.mu.Unlock()
	if !running {
		t.Fatal("the managed process was stopped when its run ended")
	}
	runs := waitForWakeRuns(t, rt, 1)
	text := userMessageTexts(t, readEventsAfter(t, base, rt.id, runs[0].AcceptedSeq-1), runs[0].RunID)
	if !strings.Contains(text, "completed exit_code=0") || !strings.Contains(text, "BUILD-READY") {
		t.Fatalf("wake user message = %q", text)
	}
	assertBackgroundNotice(t, base, rt, readEventsAfter(t, base, rt.id, runs[0].AcceptedSeq-1), runs[0].RunID)
}

// Stopping the run still stops the managed processes it started.
func TestStoppingRunStopsItsManagedProcess(t *testing.T) {
	client := &handoffClient{tool: "ManagedProcess", block: true}
	client.args = jsonArgs(t, map[string]any{"action": "start", "command": "sleep 30"})
	base, rt := newHandoffServer(t, client)
	accepted := startHandoffRun(t, base, rt)
	running := func() bool {
		rt.mu.Lock()
		defer rt.mu.Unlock()
		return rt.processManager.HasRunningProcesses()
	}
	deadline := time.Now().Add(5 * time.Second)
	for !running() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	resp := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+rt.id+"/runs/"+accepted.RunID+"/cancel", map[string]any{}, nil)
	_ = resp.Body.Close()
	waitRunTerminal(t, rt, "user-run")
	for running() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if running() {
		t.Fatal("the managed process kept running after its run was stopped")
	}
	if runs, _ := wakeRuns(rt); len(runs) != 0 {
		t.Fatalf("a stopped process woke the session: %+v", runs)
	}
}
