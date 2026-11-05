package backend

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
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

type subagentWorkflowFixture struct {
	client        *ai.OpenAI
	server        *httptest.Server
	mode          string
	mu            sync.Mutex
	parentCalls   int
	subagentCalls int
	subagentDone  chan struct{}
	parentRelease chan struct{}
	doneOnce      sync.Once
	releaseOnce   sync.Once
}

func TestServerBackgroundSubagentWorkflowAndRestore(t *testing.T) {
	for _, mode := range []string{"success", "failure"} {
		t.Run(mode, func(t *testing.T) {
			storeDir := t.TempDir()
			eventDir := t.TempDir()
			store, err := session.Open(storeDir)
			if err != nil {
				t.Fatal(err)
			}

			fixture := newSubagentWorkflowFixture(t, mode)
			backend, server := newSubagentWorkflowServer(t, store, eventDir, fixture.client)
			sess := createTestSession(t, server.URL)
			accepted := runRequest(t, server.URL, sess.ID, "subagent-workflow-1", "delegate this task")
			waitForSubagentTask(t, server.URL, sess.ID, mode)
			fixture.releaseParent()
			events := readSubagentWorkflowEvents(t, server.URL, sess.ID)
			assertSubagentWorkflowEvents(t, events, accepted.RunID, mode)

			loaded := getSession(t, server.URL, sess.ID)
			assertPersistedSubagentTask(t, store, sess.ID, loaded, mode)
			fixture.mu.Lock()
			subagentCalls := fixture.subagentCalls
			fixture.mu.Unlock()
			if subagentCalls != 1 {
				t.Fatalf("upstream subagent calls = %d, want one", subagentCalls)
			}
			if len(loaded.Tasks) != 1 {
				t.Fatalf("live task count = %d, want one task: %+v", len(loaded.Tasks), loaded.Tasks)
			}

			if err := backend.Close(); err != nil {
				t.Fatal(err)
			}
			server.Close()
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}

			store2, err := session.Open(storeDir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store2.Close() })
			backend2, server2 := newSubagentWorkflowServer(t, store2, eventDir, fixture.client)
			defer func() {
				server2.Close()
				_ = backend2.Close()
			}()

			restored := getSession(t, server2.URL, sess.ID)
			assertPersistedSubagentTask(t, store2, sess.ID, restored, mode)
			restoredEvents := readEventsAfter(t, server2.URL, sess.ID, 0)
			assertSameSubagentEventCounts(t, events, restoredEvents)
		})
	}
}

func newSubagentWorkflowFixture(t *testing.T, mode string) *subagentWorkflowFixture {
	t.Helper()
	fixture := &subagentWorkflowFixture{mode: mode, subagentDone: make(chan struct{}), parentRelease: make(chan struct{})}
	fixture.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request ai.Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode upstream request: %v", err)
			return
		}
		if len(request.Messages) == 0 {
			t.Error("upstream request had no messages")
			return
		}
		if isSubagentRequest(request) {
			fixture.mu.Lock()
			fixture.subagentCalls++
			fixture.mu.Unlock()
			if mode == "failure" {
				w.Header().Set("X-Should-Retry", "false")
				http.Error(w, `{"error":{"message":"subagent upstream failed"}}`, http.StatusBadGateway)
				fixture.signalSubagentDone()
				return
			}
			writeSubagentWorkflowStream(w, "subagent report")
			fixture.signalSubagentDone()
			return
		}

		fixture.mu.Lock()
		fixture.parentCalls++
		parentCall := fixture.parentCalls
		fixture.mu.Unlock()
		if parentCall == 1 {
			args, _ := json.Marshal(map[string]any{
				"description": "delegated task",
				"prompt":      "complete the delegated task",
				"background":  true,
			})
			writeSubagentWorkflowToolCall(w, string(args))
			return
		}
		<-fixture.subagentDone
		<-fixture.parentRelease
		writeSubagentWorkflowStream(w, "parent recovered")
	}))
	fixture.client = ai.New(fixture.server.URL, "fixture-key")
	fixture.client.MaxRetries = 1
	t.Cleanup(fixture.server.Close)
	return fixture
}

func (f *subagentWorkflowFixture) signalSubagentDone() {
	f.doneOnce.Do(func() { close(f.subagentDone) })
}

func (f *subagentWorkflowFixture) releaseParent() {
	f.releaseOnce.Do(func() { close(f.parentRelease) })
}

func isSubagentRequest(request ai.Request) bool {
	return strings.Contains(request.Messages[0].Content, "You are a subagent inside k-brain")
}

func writeSubagentWorkflowToolCall(w http.ResponseWriter, args string) {
	w.Header().Set("Content-Type", "text/event-stream")
	payload := map[string]any{
		"choices": []any{map[string]any{
			"delta": map[string]any{"tool_calls": []any{map[string]any{
				"index": 0,
				"id":    "subagent-workflow-call",
				"type":  "function",
				"function": map[string]any{
					"name":      "subagent",
					"arguments": args,
				},
			}}},
			"finish_reason": "tool_calls",
		}},
	}
	writeSubagentWorkflowSSE(w, payload)
	fmt.Fprint(w, "data: [DONE]\n\n")
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func writeSubagentWorkflowStream(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "text/event-stream")
	payload := map[string]any{
		"choices": []any{map[string]any{
			"delta":         map[string]any{"content": text},
			"finish_reason": "stop",
		}},
	}
	writeSubagentWorkflowSSE(w, payload)
	fmt.Fprint(w, "data: [DONE]\n\n")
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func writeSubagentWorkflowSSE(w io.Writer, payload any) {
	data, _ := json.Marshal(payload)
	fmt.Fprintf(w, "data: %s\n\n", data)
}

func newSubagentWorkflowServer(t *testing.T, store *session.Store, eventDir string, client ai.Client) (*Server, *httptest.Server) {
	t.Helper()
	factory := func(_ context.Context, _ string, selected protocol.ModelRef) (*agent.Agent, error) {
		ag := agent.New(client, selected.Model, 1024, "system")
		ag.ModelName = selected.Model
		ag.Provider = selected.Provider
		return ag, nil
	}
	backend, err := New(Options{
		Store:      store,
		Factory:    factory,
		EventDir:   eventDir,
		Models:     []protocol.ModelRef{{Provider: "fixture", Model: "fixture-model"}},
		DefaultCWD: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(backend)
	// Registered after the caller's store and directories, so it runs before
	// them: background subagents finish writing before either goes away.
	t.Cleanup(func() {
		server.Close()
		_ = backend.Close()
	})
	return backend, server
}

func readSubagentWorkflowEvents(t *testing.T, base, sessionID string) []protocol.Event {
	t.Helper()
	resp, err := http.Get(base + "/v1/sessions/" + sessionID + "/events?after_seq=0")
	if err != nil {
		t.Fatal(err)
	}
	return readSSE(t, resp)
}

func waitForSubagentTask(t *testing.T, base, sessionID, mode string) {
	t.Helper()
	want := string(agent.TaskDone)
	if mode == "failure" {
		want = string(agent.TaskError)
	}
	deadline := time.Now().Add(10 * time.Second)
	var last protocol.Session
	for time.Now().Before(deadline) {
		last = getSession(t, base, sessionID)
		if len(last.Tasks) == 1 && last.Tasks[0].Status == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("subagent task did not settle as %q: tasks=%+v", want, last.Tasks)
}

func readEventsAfter(t *testing.T, base, sessionID string, after int64) []protocol.Event {
	t.Helper()
	resp, err := http.Get(fmt.Sprintf("%s/v1/sessions/%s/events?after_seq=%d", base, sessionID, after))
	if err != nil {
		t.Fatal(err)
	}
	return readSSE(t, resp)
}

func assertSubagentWorkflowEvents(t *testing.T, events []protocol.Event, runID, mode string) {
	t.Helper()
	if len(events) == 0 || events[len(events)-1].Type != protocol.EventRunCompleted {
		t.Fatalf("terminal events = %+v", events)
	}
	if events[len(events)-1].RunID != runID {
		t.Fatalf("terminal run ID = %q, want %q", events[len(events)-1].RunID, runID)
	}

	var started, terminal int
	var taskID string
	for _, event := range events {
		switch event.Type {
		case protocol.EventToolCall:
			var call protocol.ToolCallEvent
			if err := json.Unmarshal(event.Payload, &call); err != nil {
				t.Fatal(err)
			}
			if call.ToolCall.Name != "subagent" {
				t.Fatalf("tool call = %+v", call.ToolCall)
			}
		case protocol.EventSubagentStarted, protocol.EventSubagentCompleted, protocol.EventSubagentFailed:
			var update protocol.SubagentEvent
			if err := json.Unmarshal(event.Payload, &update); err != nil {
				t.Fatal(err)
			}
			if taskID == "" {
				taskID = update.Subagent.ID
			} else if update.Subagent.ID != taskID {
				t.Fatalf("subagent event changed task ID from %q to %q", taskID, update.Subagent.ID)
			}
			if event.Type == protocol.EventSubagentStarted {
				started++
				if update.Subagent.Status != string(agent.TaskRunning) {
					t.Fatalf("started task status = %q", update.Subagent.Status)
				}
			} else {
				terminal++
				want := string(agent.TaskDone)
				if mode == "failure" {
					want = string(agent.TaskError)
				}
				if update.Subagent.Status != want {
					t.Fatalf("terminal task status = %q, want %q", update.Subagent.Status, want)
				}
				if mode == "failure" && update.Subagent.Error == "" {
					t.Fatal("failed subagent event omitted error")
				}
			}
		}
	}
	if started != 1 || terminal != 1 {
		t.Fatalf("subagent event counts = started %d terminal %d, events = %+v", started, terminal, events)
	}
	if taskID == "" {
		t.Fatalf("no subagent events in %+v", events)
	}
}

func assertPersistedSubagentTask(t *testing.T, store *session.Store, sessionID string, sess protocol.Session, mode string) {
	t.Helper()
	tasks, err := store.LoadTasks(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("persisted tasks = %+v, want one task", tasks)
	}
	want := string(agent.TaskDone)
	if mode == "failure" {
		want = string(agent.TaskError)
	}
	if tasks[0].Status != want {
		t.Fatalf("persisted task status = %q, want %q", tasks[0].Status, want)
	}
	if tasks[0].Model == "" || len(sess.Tasks) != 1 || sess.Tasks[0].Model.Model != tasks[0].Model {
		t.Fatalf("task model lost across persistence: stored=%+v view=%+v", tasks, sess.Tasks)
	}
	if tasks[0].ID == "" || tasks[0].Description == "" || tasks[0].Report == "" {
		t.Fatalf("persisted task lost identity or report: %+v", tasks[0])
	}
	if len(sess.Tasks) != 1 || sess.Tasks[0].ID != tasks[0].ID || sess.Tasks[0].Status != tasks[0].Status {
		t.Fatalf("session task view = %+v, persisted = %+v", sess.Tasks, tasks)
	}
}

func assertSameSubagentEventCounts(t *testing.T, before, after []protocol.Event) {
	t.Helper()
	for _, typ := range []string{protocol.EventSubagentStarted, protocol.EventSubagentCompleted, protocol.EventSubagentFailed} {
		beforeCount, afterCount := countEvents(before, typ), countEvents(after, typ)
		if beforeCount != afterCount {
			t.Fatalf("restored %s count = %d, original = %d", typ, afterCount, beforeCount)
		}
	}
}

func countEvents(events []protocol.Event, typ string) int {
	var count int
	for _, event := range events {
		if event.Type == typ {
			count++
		}
	}
	return count
}
