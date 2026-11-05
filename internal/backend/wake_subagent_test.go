package backend

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

const bgSubagentPrompt = "count the files, then report"

// bgSubagentClient plays a parent turn that starts one background subagent and
// ends, a subagent that answers only once release is closed, and wake turns that
// answer with text. With block set, the parent's second request waits until the
// run is stopped.
type bgSubagentClient struct {
	mu      sync.Mutex
	parent  int
	block   bool
	release chan struct{}
	// script, when set, answers parent calls after the first.
	script func(ctx context.Context, call int, request ai.Request) (ai.Message, error)
}

func (c *bgSubagentClient) Models(context.Context) ([]ai.ModelInfo, error) { return nil, nil }
func (c *bgSubagentClient) Complete(context.Context, ai.Request) (string, ai.Usage, error) {
	return "", ai.Usage{}, nil
}
func (c *bgSubagentClient) Clone() ai.Client   { return c }
func (c *bgSubagentClient) SetCacheKey(string) {}
func (c *bgSubagentClient) Endpoint() string   { return "http://bg-subagent-fixture.invalid" }
func (c *bgSubagentClient) Stream(ctx context.Context, request ai.Request, onText, _ func(string), _ func(string, string, string)) (ai.Message, ai.Usage, error) {
	if isBackgroundSubagentRequest(request) {
		select {
		case <-c.release:
		case <-ctx.Done():
			return ai.Message{}, ai.Usage{}, ctx.Err()
		}
		return ai.Message{Role: "assistant", Content: "SUBTASK-REPORT 42 files", StopReason: ai.StopReasonStop}, ai.Usage{}, nil
	}
	c.mu.Lock()
	c.parent++
	call, block, script := c.parent, c.block, c.script
	c.mu.Unlock()
	switch {
	case call > 1 && script != nil:
		msg, err := script(ctx, call, request)
		if err == nil && onText != nil && msg.Content != "" {
			onText(msg.Content)
		}
		return msg, ai.Usage{}, err
	case call == 1:
		tc := ai.ToolCall{ID: "spawn", Type: "function"}
		tc.Function.Name = "subagent"
		tc.Function.Arguments = `{"description":"count files","prompt":"` + bgSubagentPrompt + `","background":true,"worktree":false}`
		return ai.Message{Role: "assistant", ToolCalls: []ai.ToolCall{tc}}, ai.Usage{}, nil
	case call == 2 && block:
		<-ctx.Done()
		return ai.Message{}, ai.Usage{}, ctx.Err()
	}
	text := "started it; I will continue when it reports"
	if onText != nil {
		onText(text)
	}
	return ai.Message{Role: "assistant", Content: text, StopReason: ai.StopReasonStop}, ai.Usage{}, nil
}

func isBackgroundSubagentRequest(request ai.Request) bool {
	for _, m := range request.Messages {
		if m.Role == "user" && strings.Contains(m.TextContent(), bgSubagentPrompt) {
			return true
		}
	}
	return false
}

func backgroundTask(t *testing.T, rt *runtimeSession) agent.BackgroundTask {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if tasks := rt.agent.Tasks().List(); len(tasks) == 1 {
			return tasks[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the background subagent was not started")
	return agent.BackgroundTask{}
}

func waitTaskSettled(t *testing.T, rt *runtimeSession, id string) agent.BackgroundTask {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if snap, ok := rt.agent.Tasks().Get(id); ok && snap.Status != agent.TaskRunning {
			return snap
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("background task %s did not settle", id)
	return agent.BackgroundTask{}
}

// A background subagent keeps working after the turn that started it ends, and its
// report starts the next turn: the subagent tool promises exactly that. Ending the
// turn used to cancel it, so the session got "[subagent … cancelled] context
// canceled" instead of the report.
func TestBackgroundSubagentOutlivesRunAndWakesSession(t *testing.T) {
	client := &bgSubagentClient{release: make(chan struct{})}
	base, rt := newHandoffServer(t, client)
	startHandoffRun(t, base, rt)
	waitRunTerminal(t, rt, "user-run")
	task := backgroundTask(t, rt)
	// Give a cancellation from the run's teardown time to land.
	time.Sleep(50 * time.Millisecond)
	if snap, _ := rt.agent.Tasks().Get(task.ID); snap.Status != agent.TaskRunning {
		t.Fatalf("background subagent after its run ended = %s: %s", snap.Status, snap.Report)
	}
	close(client.release)
	if snap := waitTaskSettled(t, rt, task.ID); snap.Status != agent.TaskDone {
		t.Fatalf("background subagent = %s: %s", snap.Status, snap.Report)
	}
	runs := waitForWakeRuns(t, rt, 1)
	events := readEventsAfter(t, base, rt.id, runs[0].AcceptedSeq-1)
	text := userMessageTexts(t, events, runs[0].RunID)
	if !strings.Contains(text, "[subagent "+task.ID+" done]") || !strings.Contains(text, "SUBTASK-REPORT 42 files") {
		t.Fatalf("wake user message = %q", text)
	}
	assertBackgroundNotice(t, base, rt, events, runs[0].RunID)
}

// Stopping the run still cancels the background subagents it started.
func TestStoppingRunCancelsItsBackgroundSubagent(t *testing.T) {
	client := &bgSubagentClient{release: make(chan struct{}), block: true}
	defer close(client.release)
	base, rt := newHandoffServer(t, client)
	accepted := startHandoffRun(t, base, rt)
	task := backgroundTask(t, rt)
	resp := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+rt.id+"/runs/"+accepted.RunID+"/cancel", map[string]any{}, nil)
	_ = resp.Body.Close()
	waitRunTerminal(t, rt, "user-run")
	if snap := waitTaskSettled(t, rt, task.ID); snap.Status != agent.TaskCancelled {
		t.Fatalf("background subagent after its run was stopped = %s: %s", snap.Status, snap.Report)
	}
}

func startUserRun(t *testing.T, base string, rt *runtimeSession, requestID, prompt string) protocol.RunAccepted {
	t.Helper()
	var accepted protocol.RunAccepted
	resp := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+rt.id+"/runs", protocol.PromptRequest{
		ConversationID: rt.id, ClientRequestID: requestID, Prompt: prompt,
		Options: &protocol.RunOptions{ApprovalPolicy: "auto"},
	}, &accepted)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("run %s while a background subagent works: status %d", requestID, resp.StatusCode)
	}
	return accepted
}

func textReply(text string) ai.Message {
	return ai.Message{Role: "assistant", Content: text, StopReason: ai.StopReasonStop}
}

// backgroundReports returns the history's user messages carrying the subagent's report.
func backgroundReports(rt *runtimeSession) []ai.Message {
	rt.mu.Lock()
	ag := rt.agent
	rt.mu.Unlock()
	var out []ai.Message
	for _, m := range ag.MessagesSnapshot() {
		if m.Role == "user" && strings.Contains(m.TextContent(), "SUBTASK-REPORT 42 files") {
			out = append(out, m)
		}
	}
	return out
}

// A background subagent no longer holds the session: the user starts and finishes
// another run while it works, and its report then wakes the session. The run used to
// be refused with "session already has a running turn" until the subagent ended.
func TestUserRunCompletesWhileBackgroundSubagentRunsThenReportWakes(t *testing.T) {
	client := &bgSubagentClient{release: make(chan struct{})}
	client.script = func(context.Context, int, ai.Request) (ai.Message, error) { return textReply("answered"), nil }
	base, rt := newHandoffServer(t, client)
	startHandoffRun(t, base, rt)
	waitRunTerminal(t, rt, "user-run")
	task := backgroundTask(t, rt)

	startUserRun(t, base, rt, "user-run-2", "an unrelated question")
	waitRunTerminal(t, rt, "user-run-2")
	if snap, _ := rt.agent.Tasks().Get(task.ID); snap.Status != agent.TaskRunning {
		t.Fatalf("background subagent after the second run = %s: %s", snap.Status, snap.Report)
	}
	if runs, _ := wakeRuns(rt); len(runs) != 0 {
		t.Fatalf("wake runs before the report = %+v", runs)
	}

	close(client.release)
	if snap := waitTaskSettled(t, rt, task.ID); snap.Status != agent.TaskDone {
		t.Fatalf("background subagent = %s: %s", snap.Status, snap.Report)
	}
	runs := waitForWakeRuns(t, rt, 1)
	events := readEventsAfter(t, base, rt.id, runs[0].AcceptedSeq-1)
	if text := userMessageTexts(t, events, runs[0].RunID); !strings.Contains(text, "[subagent "+task.ID+" done]") || !strings.Contains(text, "SUBTASK-REPORT 42 files") {
		t.Fatalf("wake user message = %q", text)
	}
	if reports := backgroundReports(rt); len(reports) != 1 || reports[0].Origin != ai.OriginBackground {
		t.Fatalf("report in history = %+v", reports)
	}
}

// A report that arrives while the user's run is still working is steered into that
// run as a background notice; no wake run is needed.
func TestBackgroundReportSteersIntoRunningUserRun(t *testing.T) {
	client := &bgSubagentClient{release: make(chan struct{})}
	entered, gate := make(chan struct{}), make(chan struct{})
	var steered ai.Request
	client.script = func(ctx context.Context, call int, request ai.Request) (ai.Message, error) {
		switch call {
		case 2:
			return textReply("started it"), nil
		case 3:
			close(entered)
			select {
			case <-gate:
			case <-ctx.Done():
				return ai.Message{}, ctx.Err()
			}
			return textReply("still answering"), nil
		}
		steered = request
		return textReply("folded the report in"), nil
	}
	base, rt := newHandoffServer(t, client)
	startHandoffRun(t, base, rt)
	waitRunTerminal(t, rt, "user-run")
	task := backgroundTask(t, rt)

	startUserRun(t, base, rt, "user-run-2", "an unrelated question")
	<-entered
	close(client.release)
	if snap := waitTaskSettled(t, rt, task.ID); snap.Status != agent.TaskDone {
		t.Fatalf("background subagent = %s: %s", snap.Status, snap.Report)
	}
	// The notice follows the settle on the subagent's goroutine.
	time.Sleep(100 * time.Millisecond)
	close(gate)
	waitRunTerminal(t, rt, "user-run-2")

	found := false
	for _, m := range steered.Messages {
		if m.Role == "user" && strings.Contains(m.TextContent(), "SUBTASK-REPORT 42 files") {
			found = m.Origin == ai.OriginBackground
		}
	}
	if !found {
		t.Fatalf("the run's next model request lacks the background report: %+v", steered.Messages)
	}
	time.Sleep(100 * time.Millisecond)
	if runs, _ := wakeRuns(rt); len(runs) != 0 {
		t.Fatalf("a steered report also started wake runs: %+v", runs)
	}
	if reports := backgroundReports(rt); len(reports) != 1 {
		t.Fatalf("report delivered %d times", len(reports))
	}
}

// A wake run that loses the session to a user run keeps its notices and delivers
// them once that run ends.
func TestWakeRunConflictRequeuesBehindUserRun(t *testing.T) {
	client := &bgSubagentClient{release: make(chan struct{})}
	entered, gate := make(chan struct{}), make(chan struct{})
	client.script = func(ctx context.Context, call int, _ ai.Request) (ai.Message, error) {
		if call == 2 {
			close(entered)
			select {
			case <-gate:
			case <-ctx.Done():
				return ai.Message{}, ctx.Err()
			}
		}
		return textReply("ok"), nil
	}
	defer close(client.release)
	base, rt, srv := newHandoffServerWith(t, client)
	startUserRun(t, base, rt, "user-run-2", "an unrelated question")
	<-entered
	rt.leases.Add(1)
	srv.startWakeRun(rt, []string{"[wait w1 done] READY"}, wakeTemplate{})
	rt.wake.mu.Lock()
	pending := append([]string(nil), rt.wake.texts...)
	rt.wake.mu.Unlock()
	if len(pending) != 1 || pending[0] != "[wait w1 done] READY" {
		t.Fatalf("notices after the conflicting wake = %q", pending)
	}
	close(gate)
	runs := waitForWakeRuns(t, rt, 1)
	events := readEventsAfter(t, base, rt.id, runs[0].AcceptedSeq-1)
	if text := userMessageTexts(t, events, runs[0].RunID); !strings.Contains(text, "[wait w1 done] READY") {
		t.Fatalf("wake user message = %q", text)
	}
}

// Deleting the session cancels the background subagents still working for it, and
// their settling writes nothing back.
func TestDeletingSessionCancelsBackgroundSubagent(t *testing.T) {
	client := &bgSubagentClient{release: make(chan struct{})}
	defer close(client.release)
	base, rt, srv := newHandoffServerWith(t, client)
	startHandoffRun(t, base, rt)
	waitRunTerminal(t, rt, "user-run")
	task := backgroundTask(t, rt)
	registry := rt.agent.Tasks()

	if status := doJSON(t, http.MethodDelete, base+"/v1/sessions/"+rt.id, nil, nil); status != http.StatusOK {
		t.Fatalf("DELETE while a background subagent works: status %d", status)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		snap, _ := registry.Get(task.ID)
		if snap.Status == agent.TaskCancelled {
			break
		}
		if snap.Status != agent.TaskRunning || time.Now().After(deadline) {
			t.Fatalf("background subagent after delete = %s: %s", snap.Status, snap.Report)
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	if _, _, err := srv.store.Load(rt.id); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("deleted session after its subagent settled: %v", err)
	}
	if runs, _ := wakeRuns(rt); len(runs) != 0 {
		t.Fatalf("a deleted session woke: %+v", runs)
	}
}

// Closing the session ends its background subagents too.
func TestClosingSessionCancelsBackgroundSubagent(t *testing.T) {
	client := &bgSubagentClient{release: make(chan struct{})}
	defer close(client.release)
	base, rt := newHandoffServer(t, client)
	startHandoffRun(t, base, rt)
	waitRunTerminal(t, rt, "user-run")
	task := backgroundTask(t, rt)
	resp := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+rt.id+"/close", map[string]any{}, nil)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("close status %d", resp.StatusCode)
	}
	if snap := waitTaskSettled(t, rt, task.ID); snap.Status != agent.TaskCancelled {
		t.Fatalf("background subagent after close = %s: %s", snap.Status, snap.Report)
	}
}

// Work that would orphan or race a running subagent waits for it: a model switch
// rebuilds the agent that owns the subagent registry, and a checkpoint rewind
// restores files the subagent may still be writing.
func TestModelSwitchAndRewindWaitForBackgroundSubagent(t *testing.T) {
	client := &bgSubagentClient{release: make(chan struct{})}
	client.script = func(context.Context, int, ai.Request) (ai.Message, error) { return textReply("answered"), nil }
	base, rt := newHandoffServer(t, client)
	startHandoffRun(t, base, rt)
	waitRunTerminal(t, rt, "user-run")
	task := backgroundTask(t, rt)
	ag := rt.agent

	other := protocol.ModelRef{Provider: "fixture", Model: "other-model"}
	resp := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+rt.id+"/runs", protocol.PromptRequest{ConversationID: rt.id, ClientRequestID: "switch-run", Prompt: "hi", Model: &other}, nil)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("run with a model switch while a subagent works: status %d", resp.StatusCode)
	}
	if status := doJSON(t, http.MethodPatch, base+"/v1/sessions/"+rt.id, map[string]any{"model": other}, nil); status != http.StatusConflict {
		t.Fatalf("PATCH model while a subagent works: status %d", status)
	}
	if status := doJSON(t, http.MethodPost, base+"/v1/sessions/"+rt.id+"/checkpoints/1/rewind", map[string]any{}, nil); status != http.StatusConflict {
		t.Fatalf("rewind while a subagent works: status %d", status)
	}
	if rt.agent != ag {
		t.Fatal("the agent owning the running subagent was replaced")
	}
	close(client.release)
	waitTaskSettled(t, rt, task.ID)
	waitForWakeRuns(t, rt, 1)
	if status := doJSON(t, http.MethodPatch, base+"/v1/sessions/"+rt.id, map[string]any{"model": other}, nil); status != http.StatusOK {
		t.Fatalf("PATCH model after the subagent finished: status %d", status)
	}
}
