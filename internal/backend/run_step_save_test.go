package backend

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

// toolRoundClient answers the first request with one tool call and blocks on
// later requests until their context ends.
type toolRoundClient struct {
	mu      sync.Mutex
	calls   int
	waiting chan struct{}
}

func (c *toolRoundClient) Models(context.Context) ([]ai.ModelInfo, error) { return nil, nil }
func (c *toolRoundClient) Complete(context.Context, ai.Request) (string, ai.Usage, error) {
	return "", ai.Usage{}, nil
}
func (c *toolRoundClient) Clone() ai.Client   { return c }
func (c *toolRoundClient) SetCacheKey(string) {}
func (c *toolRoundClient) Endpoint() string   { return "http://fixture.invalid" }
func (c *toolRoundClient) Stream(ctx context.Context, _ ai.Request, onText, _ func(string), _ func(string, string, string)) (ai.Message, ai.Usage, error) {
	c.mu.Lock()
	c.calls++
	first := c.calls == 1
	c.mu.Unlock()
	if first {
		if onText != nil {
			onText("Checking.")
		}
		call := ai.ToolCall{ID: "call-1", Type: "function"}
		call.Function.Name, call.Function.Arguments = "no_such_tool", "{}"
		return ai.Message{Role: "assistant", Content: "Checking.", StopReason: ai.StopReasonToolUse, ToolCalls: []ai.ToolCall{call}}, ai.Usage{}, nil
	}
	c.waiting <- struct{}{}
	<-ctx.Done()
	return ai.Message{}, ai.Usage{}, ctx.Err()
}

// History is saved after each finished tool round, so the steps a run
// completed are on disk before the run ends.
func TestServerSavesHistoryAfterEachToolRound(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	client := &toolRoundClient{waiting: make(chan struct{}, 1)}
	_, httpServer := startProviderBackend(t, store, t.TempDir(), client)
	defer httpServer.Close()
	sess := createTestSession(t, httpServer.URL)
	accepted := runRequest(t, httpServer.URL, sess.ID, "step-save", "check it")
	select {
	case <-client.waiting:
	case <-time.After(10 * time.Second):
		t.Fatal("second model request was not sent")
	}
	_, saved, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	roles := []string{}
	for _, message := range saved {
		if message.Role != "system" {
			roles = append(roles, message.Role)
		}
	}
	if len(roles) != 3 || roles[0] != protocol.RoleUser || roles[1] != protocol.RoleAssistant || roles[2] != protocol.RoleTool {
		t.Fatalf("history saved mid-run = %v (%+v)", roles, saved)
	}
	if tool := saved[len(saved)-1]; tool.ToolCallID != "call-1" {
		t.Fatalf("saved tool result = %+v", tool)
	}
	cancelRun(t, httpServer.URL, sess.ID, accepted.RunID)
	if events := waitForRun(t, httpServer.URL, sess.ID, 0); events[len(events)-1].Type != protocol.EventRunCancelled {
		t.Fatalf("events = %+v", events)
	}
}
