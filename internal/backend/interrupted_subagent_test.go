package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

// Keep the child in cancellation cleanup until the test releases it. This models
// a provider/tool that observes cancellation before finishing its own cleanup.
type interruptedSubagentClient struct {
	scriptedClient
	background  bool
	started     chan struct{}
	cancelled   chan struct{}
	release     chan struct{}
	parentCalls int
}

func (c *interruptedSubagentClient) Clone() ai.Client { return c }
func (c *interruptedSubagentClient) Stream(ctx context.Context, req ai.Request, onText, onThink func(string), onTool func(string, string, string)) (ai.Message, ai.Usage, error) {
	if isSubagentRequest(req) {
		close(c.started)
		<-ctx.Done()
		close(c.cancelled)
		<-c.release
		return ai.Message{}, ai.Usage{}, ctx.Err()
	}
	c.mu.Lock()
	c.parentCalls++
	call := c.parentCalls
	c.mu.Unlock()
	if call == 1 {
		args, _ := json.Marshal(map[string]any{"prompt": "work until cancelled", "description": "cancel fixture", "background": c.background})
		tc := ai.ToolCall{ID: "delegate", Type: "function"}
		tc.Function.Name, tc.Function.Arguments = "subagent", string(args)
		return ai.Message{Role: "assistant", ToolCalls: []ai.ToolCall{tc}, StopReason: ai.StopReasonToolUse}, ai.Usage{}, nil
	}
	if call == 2 && c.background {
		<-ctx.Done()
		return ai.Message{}, ai.Usage{}, ctx.Err()
	}
	return c.scriptedClient.Stream(ctx, req, onText, onThink, onTool)
}

func TestInterruptedSubagentTerminalWaitsForSettlement(t *testing.T) {
	for _, background := range []bool{false, true} {
		name := "foreground"
		if background {
			name = "background"
		}
		t.Run(name, func(t *testing.T) {
			store, err := session.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			client := &interruptedSubagentClient{background: background, started: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{}), scriptedClient: scriptedClient{response: "next reply"}}
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(client.release) }) }
			defer release()
			eventDir := t.TempDir()
			backend, server := newSubagentWorkflowServer(t, store, eventDir, client)
			defer server.Close()
			defer func() { release(); _ = backend.Close() }()
			sess := createTestSession(t, server.URL)
			rt, err := backend.loadRuntimeByID(sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			defer backend.releaseRuntime(rt)
			// This regression isolates cancellation settlement and the next user run.
			// Automatic notice delivery is covered by wake_test.go; let the notice
			// remain pending here rather than racing that next run with a wake run.
			rt.agent.OnOrphanedSteer = nil
			accepted := runRequest(t, server.URL, sess.ID, "interrupted", "delegate")
			select {
			case <-client.started:
			case <-time.After(5 * time.Second):
				t.Fatal("child did not start")
			}
			resp := postJSON(t, http.DefaultClient, server.URL+"/v1/sessions/"+sess.ID+"/runs/"+accepted.RunID+"/cancel", map[string]any{}, nil)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("cancel status = %d", resp.StatusCode)
			}
			select {
			case <-client.cancelled:
			case <-time.After(5 * time.Second):
				t.Fatal("child did not inherit cancellation")
			}
			// A terminal tells clients they may submit the next run. It must not
			// escape while the cancelled child still keeps that run busy.
			timer := time.NewTimer(150 * time.Millisecond)
			defer timer.Stop()
			earlyTerminal := false
		waiting:
			for {
				rt.mu.Lock()
				events, eventErr := rt.eventsAfterLocked(0)
				changed := rt.changed
				rt.mu.Unlock()
				if eventErr != nil {
					t.Fatal(eventErr)
				}
				for _, e := range events {
					if e.Type == protocol.EventRunCancelled {
						earlyTerminal = true
						break waiting
					}
				}
				select {
				case <-changed:
				case <-timer.C:
					break waiting
				}
			}
			path := server.URL + "/v1/sessions/" + sess.ID + "/runs"
			if code := doJSON(t, http.MethodPost, path, protocol.PromptRequest{ClientRequestID: "rejected", Prompt: "interrupt again"}, nil); code != http.StatusConflict {
				t.Fatalf("new run during cancellation cleanup = %d, want 409", code)
			}
			if code := doJSON(t, http.MethodGet, path+"?client_request_id=rejected", nil, nil); code != http.StatusNotFound {
				t.Fatalf("rejected request acquired a run: lookup = %d", code)
			}
			release()
			events := readEventsAfter(t, server.URL, sess.ID, 0)
			if earlyTerminal {
				t.Error("run.cancelled published before cancelled child finished cleanup")
			}
			if len(events) == 0 || events[len(events)-1].Type != protocol.EventRunCancelled {
				t.Fatalf("missing cancelled terminal: %+v", events)
			}
			if background {
				found := false
				for _, e := range events[:len(events)-1] {
					if e.Type == protocol.EventSubagentFailed {
						found = true
					}
				}
				if !found {
					t.Error("cancelled child settlement absent before run terminal")
				}
			}
			// The same session accepts another run immediately after the terminal.
			next := runRequest(t, server.URL, sess.ID, "after-interruption", "continue")
			if next.RunID == accepted.RunID {
				t.Fatal("next request reused cancelled run")
			}
			readEventsAfter(t, server.URL, sess.ID, next.AcceptedSeq-1)
			loaded := getSession(t, server.URL, sess.ID)
			if background && (len(loaded.Tasks) != 1 || loaded.Tasks[0].Status != "cancelled") {
				t.Fatalf("task persistence = %+v", loaded.Tasks)
			}
			if code := doJSON(t, http.MethodPost, path+"/"+accepted.RunID+"/cancel", map[string]any{}, nil); code != http.StatusConflict {
				t.Fatalf("old run cancellation = %d, want 409", code)
			}
			server.Close()
			if err := backend.Close(); err != nil {
				t.Fatal(err)
			}
			restoredBackend, restoredServer := newSubagentWorkflowServer(t, store, eventDir, client)
			defer restoredServer.Close()
			defer restoredBackend.Close()
			restored := getSession(t, restoredServer.URL, sess.ID)
			if background && (len(restored.Tasks) != 1 || restored.Tasks[0].Status != "cancelled") {
				t.Fatalf("restored task = %+v", restored.Tasks)
			}
			restoredRuntime, err := restoredBackend.loadRuntimeByID(sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			defer restoredBackend.releaseRuntime(restoredRuntime)
			restoredRuntime.mu.Lock()
			runCount := len(restoredRuntime.runs)
			restoredRuntime.mu.Unlock()
			if runCount != 2 {
				t.Fatalf("accepted runs = %d; rejected request or completion Steer created another run", runCount)
			}
			restoredEvents := readEventsAfter(t, restoredServer.URL, sess.ID, next.AcceptedSeq-1)
			if len(restoredEvents) == 0 || restoredEvents[len(restoredEvents)-1].Type != protocol.EventRunCompleted {
				t.Fatalf("next run was not persisted: %+v", restoredEvents)
			}
		})
	}
}
