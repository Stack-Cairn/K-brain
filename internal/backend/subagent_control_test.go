package backend

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

type subagentControlClient struct {
	scriptedClient
	id          string
	parentCalls int
}

func (c *subagentControlClient) Clone() ai.Client { return c }
func (c *subagentControlClient) Stream(ctx context.Context, req ai.Request, onText, onThink func(string), onCall func(string, string, string)) (ai.Message, ai.Usage, error) {
	if isSubagentRequest(req) {
		return c.scriptedClient.Stream(ctx, req, onText, onThink, onCall)
	}
	c.parentCalls++
	if c.parentCalls == 1 {
		args, _ := json.Marshal(map[string]string{"id": c.id, "message": "followup instructions"})
		call := ai.ToolCall{ID: "followup-call", Type: "function"}
		call.Function.Name = "subagent_followup"
		call.Function.Arguments = string(args)
		return ai.Message{Role: "assistant", StopReason: ai.StopReasonToolUse, ToolCalls: []ai.ToolCall{call}}, ai.Usage{}, nil
	}
	return ai.Message{Role: "assistant", Content: "parent done", StopReason: ai.StopReasonStop}, ai.Usage{}, nil
}

func TestCanonicalRunCanFollowupOwnedSubagent(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	client := &subagentControlClient{scriptedClient: scriptedClient{response: "child report"}}
	s, err := New(Options{Store: store, EventDir: t.TempDir(), DefaultCWD: t.TempDir(), Factory: func(_ context.Context, _ string, m protocol.ModelRef) (*agent.Agent, error) {
		return agent.New(client, m.Model, 1024, "system"), nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(s)
	defer hs.Close()
	defer s.Close()
	sess := createTestSession(t, hs.URL)
	rt, err := s.loadRuntimeByID(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer s.releaseRuntime(rt)
	// This test drives the follow-up turn itself; keep the completion notice from
	// starting a wake run first (wake_test.go covers that path).
	rt.agent.OnOrphanedSteer = nil
	task := rt.agent.StartBackground("original task", "original instructions", agent.SubModel{})
	select {
	case <-task.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("child did not finish")
	}
	client.id = task.ID
	accepted := runRequest(t, hs.URL, sess.ID, "followup-request", "continue the task")
	events := readEventsAfter(t, hs.URL, sess.ID, accepted.AcceptedSeq-1)
	if !hasEvent(events, protocol.EventRunCompleted) {
		t.Fatal("parent run did not complete")
	}
	found := false
	childRequest := false
	for _, e := range events {
		if e.Type == protocol.EventToolResult && strings.Contains(string(e.Payload), "child report") {
			found = true
		}
		if e.Type == "trajectory.request.started" && strings.Contains(string(e.Payload), task.ID) {
			childRequest = true
		}
	}
	if !found || !childRequest {
		t.Fatalf("tool result=%v child attribution=%v", found, childRequest)
	}
	snap, _ := rt.agent.Tasks().Get(task.ID)
	hasOriginal, hasFollowup := false, false
	for _, m := range snap.SubMessages {
		hasOriginal = hasOriginal || m.Content == "original instructions"
		hasFollowup = hasFollowup || m.Content == "followup instructions"
	}
	if !hasOriginal || !hasFollowup {
		t.Fatal("child conversation not retained")
	}
	tasks, err := store.LoadTasks(sess.ID)
	if err != nil || len(tasks) != 1 || tasks[0].ID != task.ID {
		t.Fatalf("persisted tasks=%v err=%v", tasks, err)
	}
}
