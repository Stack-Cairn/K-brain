package backend

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
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
	call, block := c.parent, c.block
	c.mu.Unlock()
	switch {
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
