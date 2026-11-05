package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func eventsOfType(events []protocol.Event, typ string) []protocol.Event {
	var out []protocol.Event
	for _, e := range events {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

func subagentPayload(t *testing.T, e protocol.Event) protocol.Subagent {
	t.Helper()
	var payload protocol.SubagentEvent
	if err := json.Unmarshal(e.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	return payload.Subagent
}

func getSubagents(t *testing.T, base, id string) protocol.SubagentList {
	t.Helper()
	var out protocol.SubagentList
	trajectoryGET(t, base+"/v1/sessions/"+id+"/subagents", http.StatusOK, &out)
	return out
}

func postSubagentCancel(t *testing.T, base, id, task string) int {
	t.Helper()
	resp, err := http.Post(base+"/v1/sessions/"+id+"/subagents/"+task+"/cancel", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// A foreground subagent reports its description, model and parent tool call,
// journals its own text and tools, and its transcript reads back over HTTP.
func TestForegroundSubagentLiveTranscript(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request ai.Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		last := request.Messages[len(request.Messages)-1]
		if isSubagentRequest(request) {
			w.Header().Set("Content-Type", "text/event-stream")
			if last.Role == "tool" {
				for _, chunk := range []string{"child ", "tool ", "handled"} {
					writeToolHistorySSE(w, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": chunk}}}})
				}
				writeToolHistorySSE(w, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": "stop"}}})
				fmt.Fprint(w, "data: [DONE]\n\n")
				return
			}
			args, _ := json.Marshal(map[string]string{"command": "printf 'child-output\\n'"})
			writeToolHistorySSE(w, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "checking first"}}}})
			writeToolHistorySSE(w, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "child-bash", "type": "function", "function": map[string]any{"name": "bash", "arguments": string(args)}}}}, "finish_reason": "tool_calls"}}})
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		if last.Role == "tool" {
			writeSubagentWorkflowStream(w, "parent done")
			return
		}
		args, _ := json.Marshal(map[string]any{"description": "foreground worker", "prompt": "perform child bash", "background": false})
		writeSubagentWorkflowToolCall(w, string(args))
	}))
	defer upstream.Close()
	client := ai.New(upstream.URL, "fixture-key")
	client.MaxRetries = 0
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, server := newToolHistoryServer(t, store, t.TempDir(), client)
	sess := createTestSession(t, server.URL)
	accepted := runRequest(t, server.URL, sess.ID, "foreground-transcript", "delegate tool")
	events := readTrajectoryRun(t, server.URL, sess.ID, accepted, "allow_once")

	started := eventsOfType(events, protocol.EventSubagentStarted)
	completed := eventsOfType(events, protocol.EventSubagentCompleted)
	if len(started) != 1 || len(completed) != 1 {
		t.Fatalf("subagent events: started=%d completed=%d", len(started), len(completed))
	}
	start := subagentPayload(t, started[0])
	if start.Description != "foreground worker" || start.ParentToolCallID != "subagent-workflow-call" || start.Model.Model != "fixture-model" || start.Background {
		t.Fatalf("started payload = %+v", start)
	}
	end := subagentPayload(t, completed[0])
	if end.ID != start.ID || end.Status != string(agent.TaskDone) || end.ToolCount != 1 || end.ParentToolCallID != "subagent-workflow-call" || end.StartedAt == nil || end.EndedAt == nil || end.Description != "foreground worker" {
		t.Fatalf("completed payload = %+v", end)
	}
	texts := eventsOfType(events, protocol.EventChildText)
	var joined strings.Builder
	for _, e := range texts {
		var p protocol.ChildTextEvent
		_ = json.Unmarshal(e.Payload, &p)
		if p.TaskID != start.ID {
			t.Fatalf("child text for %q, want %q", p.TaskID, start.ID)
		}
		joined.WriteString(p.Text)
	}
	if joined.String() != "checking firstchild tool handled" || len(texts) > 3 {
		t.Fatalf("child text events (%d): %q", len(texts), joined.String())
	}
	// The coalesced text before the tool lands ahead of the tool start.
	var order []string
	for _, e := range events {
		if e.Type == protocol.EventChildText || e.Type == protocol.EventChildToolStart || e.Type == protocol.EventChildToolEnd {
			order = append(order, e.Type)
		}
	}
	if len(order) < 3 || order[0] != protocol.EventChildText || order[1] != protocol.EventChildToolStart || order[2] != protocol.EventChildToolEnd {
		t.Fatalf("child event order = %v", order)
	}

	list := getSubagents(t, server.URL, sess.ID)
	if len(list.Subagents) != 1 || list.Subagents[0].ID != start.ID || list.Subagents[0].ToolCount != 1 || list.Subagents[0].Status != string(agent.TaskDone) || list.Subagents[0].ParentToolCallID != "subagent-workflow-call" || list.Subagents[0].DurationMS < 0 || list.LastSeq == 0 {
		t.Fatalf("subagent list = %+v", list)
	}

	var transcript protocol.SubagentTranscript
	trajectoryGET(t, server.URL+"/v1/sessions/"+sess.ID+"/subagents/"+start.ID, http.StatusOK, &transcript)
	if transcript.Live || transcript.LastSeq != list.LastSeq || transcript.Subagent.ID != start.ID {
		t.Fatalf("transcript header = %+v", transcript)
	}
	roles := make([]string, len(transcript.Messages))
	for i, m := range transcript.Messages {
		roles[i] = m.Role
	}
	if strings.Join(roles, ",") != "user,assistant,tool,assistant" {
		t.Fatalf("transcript roles = %v (%+v)", roles, transcript.Messages)
	}
	msgs := transcript.Messages
	if msgs[0].Content[0].Text != "perform child bash" || msgs[1].Content[0].Text != "checking first" || len(msgs[1].ToolCalls) != 1 || msgs[1].ToolCalls[0].Name != "bash" || msgs[2].ToolCallID != "child-bash" || !strings.Contains(msgs[2].Content[0].Text, "child-output") || msgs[3].Content[0].Text != "child tool handled" || msgs[3].Model != "fixture-model" {
		t.Fatalf("transcript = %+v", msgs)
	}

	trajectoryGET(t, server.URL+"/v1/sessions/"+sess.ID+"/subagents/missing-1", http.StatusNotFound, nil)
	if code := postSubagentCancel(t, server.URL, sess.ID, start.ID); code != http.StatusConflict {
		t.Fatalf("cancel finished subagent = %d", code)
	}
	if code := postSubagentCancel(t, server.URL, sess.ID, "missing-1"); code != http.StatusNotFound {
		t.Fatalf("cancel unknown subagent = %d", code)
	}
}

// Stopping a running foreground subagent ends it alone: the parent turn goes
// on with a failed tool result. While it runs, its transcript and its own
// event stream are available.
func TestForegroundSubagentStopKeepsParentRunning(t *testing.T) {
	childWaiting := make(chan struct{}, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request ai.Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		last := request.Messages[len(request.Messages)-1]
		if isSubagentRequest(request) {
			w.Header().Set("Content-Type", "text/event-stream")
			writeToolHistorySSE(w, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "partial child text"}}}})
			w.(http.Flusher).Flush()
			select {
			case childWaiting <- struct{}{}:
			default:
			}
			<-r.Context().Done()
			return
		}
		if last.Role == "tool" {
			if !strings.Contains(last.Content, agent.ErrSubagentStopped.Error()) {
				t.Errorf("parent tool result = %q", last.Content)
			}
			writeSubagentWorkflowStream(w, "parent continued")
			return
		}
		args, _ := json.Marshal(map[string]any{"description": "slow worker", "prompt": "take your time"})
		writeSubagentWorkflowToolCall(w, string(args))
	}))
	defer upstream.Close()
	client := ai.New(upstream.URL, "fixture-key")
	client.MaxRetries = 0
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, server := newToolHistoryServer(t, store, t.TempDir(), client)
	sess := createTestSession(t, server.URL)
	accepted := runRequest(t, server.URL, sess.ID, "foreground-stop", "delegate")
	select {
	case <-childWaiting:
	case <-time.After(10 * time.Second):
		t.Fatal("subagent request never arrived")
	}
	var list protocol.SubagentList
	deadline := time.Now().Add(5 * time.Second)
	for {
		list = getSubagents(t, server.URL, sess.ID)
		if len(list.Subagents) == 1 && list.Subagents[0].Status == string(agent.TaskRunning) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("running subagent not listed: %+v", list)
		}
		time.Sleep(10 * time.Millisecond)
	}
	id := list.Subagents[0].ID
	// Wait for the coalesced text to be journaled.
	var transcript protocol.SubagentTranscript
	for {
		trajectoryGET(t, server.URL+"/v1/sessions/"+sess.ID+"/subagents/"+id, http.StatusOK, &transcript)
		if len(transcript.Messages) == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("mid-run transcript = %+v", transcript)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !transcript.Live || transcript.Messages[0].Content[0].Text != "take your time" || transcript.Messages[1].Content[0].Text != "partial child text" {
		t.Fatalf("mid-run transcript = %+v", transcript)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/v1/sessions/%s/subagents/%s/events?after_seq=%d", server.URL, sess.ID, id, transcript.LastSeq), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	streamed := make(chan []protocol.Event, 1)
	go func() { streamed <- readSSEUntil(ctx, resp) }()

	if code := postSubagentCancel(t, server.URL, sess.ID, id); code != http.StatusAccepted {
		t.Fatalf("cancel running subagent = %d", code)
	}
	var tail []protocol.Event
	select {
	case tail = <-streamed:
	case <-ctx.Done():
		t.Fatal("subagent event stream did not end")
	}
	if len(tail) == 0 || tail[len(tail)-1].Type != protocol.EventSubagentFailed {
		t.Fatalf("subagent stream tail = %+v", tail)
	}
	for _, e := range tail {
		if subagentEventID(e) != id {
			t.Fatalf("foreign event on subagent stream: %+v", e)
		}
	}
	stopped := subagentPayload(t, tail[len(tail)-1])
	if stopped.Status != string(agent.TaskCancelled) || stopped.Error != agent.ErrSubagentStopped.Error() {
		t.Fatalf("stopped payload = %+v", stopped)
	}
	events := readEventsAfter(t, server.URL, sess.ID, accepted.AcceptedSeq-1)
	if len(events) == 0 || events[len(events)-1].Type != protocol.EventRunCompleted {
		t.Fatalf("parent run did not complete: %+v", events)
	}
}

// Background subagents carry their parent tool call through the store, and a
// finished one serves its stored transcript.
func TestBackgroundSubagentParentToolCallAndTranscript(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fixture := newSubagentWorkflowFixture(t, "success")
	_, server := newSubagentWorkflowServer(t, store, t.TempDir(), fixture.client)
	defer server.Close()
	sess := createTestSession(t, server.URL)
	runRequest(t, server.URL, sess.ID, "background-transcript", "delegate")
	waitForSubagentTask(t, server.URL, sess.ID, "success")
	fixture.releaseParent()
	readSubagentWorkflowEvents(t, server.URL, sess.ID)

	loaded := getSession(t, server.URL, sess.ID)
	task := loaded.Tasks[0]
	if task.ParentToolCallID != "subagent-workflow-call" || !task.Background || task.DurationMS < 0 {
		t.Fatalf("session task = %+v", task)
	}
	list := getSubagents(t, server.URL, sess.ID)
	if len(list.Subagents) != 1 || list.Subagents[0].ID != task.ID || !list.Subagents[0].Background || list.Subagents[0].ParentToolCallID != "subagent-workflow-call" || list.Subagents[0].Description != "delegated task" || list.Subagents[0].Status != string(agent.TaskDone) {
		t.Fatalf("subagent list = %+v", list)
	}
	var transcript protocol.SubagentTranscript
	trajectoryGET(t, server.URL+"/v1/sessions/"+sess.ID+"/subagents/"+task.ID, http.StatusOK, &transcript)
	last := transcript.Messages[len(transcript.Messages)-1]
	if transcript.Live || last.Role != protocol.RoleAssistant || len(last.Content) == 0 || last.Content[0].Text != "subagent report" {
		t.Fatalf("background transcript = %+v", transcript)
	}
	for _, m := range transcript.Messages {
		if m.Role == protocol.RoleSystem || m.Role == protocol.RoleDeveloper {
			t.Fatalf("transcript leaks %s message", m.Role)
		}
	}
}

func newChildTestRuntime(t *testing.T) *runtimeSession {
	t.Helper()
	return &runtimeSession{id: "child-runtime", eventDir: t.TempDir(), changed: make(chan struct{})}
}

// Text deltas are journaled in chunks, and a tool call never overtakes the
// text before it.
func TestChildTextCoalescing(t *testing.T) {
	rt := newChildTestRuntime(t)
	ev := rt.trajectoryChildEventsForRun("sub-1", "run-1")
	for i := 0; i < 50; i++ {
		ev.OnThink("t")
	}
	for i := 0; i < 50; i++ {
		ev.OnText("x")
	}
	ev.OnToolStart("call-1", "Read", `{"path":"a"}`)
	ev.OnToolResult("call-1", "Read", tools.Result{Text: "ok"})
	ev.OnText(strings.Repeat("y", childFlushBytes))
	ev.OnText("z")
	deadline := time.Now().Add(2 * time.Second)
	for {
		rt.mu.Lock()
		n := len(rt.events)
		rt.mu.Unlock()
		if n == 6 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("events = %d", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
	rt.mu.Lock()
	events := append([]protocol.Event(nil), rt.events...)
	rt.mu.Unlock()
	want := []string{protocol.EventChildThinking, protocol.EventChildText, protocol.EventChildToolStart, protocol.EventChildToolEnd, protocol.EventChildText, protocol.EventChildText}
	for i, e := range events {
		if e.Type != want[i] || e.RunID != "run-1" || subagentEventID(e) != "sub-1" {
			t.Fatalf("event %d = %s/%s, want %s", i, e.Type, e.RunID, want[i])
		}
	}
	var text protocol.ChildTextEvent
	_ = json.Unmarshal(events[1].Payload, &text)
	if text.Text != strings.Repeat("x", 50) {
		t.Fatalf("coalesced text = %q", text.Text)
	}
	_ = json.Unmarshal(events[5].Payload, &text)
	if text.Text != "z" {
		t.Fatalf("text after a full chunk = %q", text.Text)
	}
	if rt.subagents["sub-1"].toolCount != 1 {
		t.Fatalf("tool count = %d", rt.subagents["sub-1"].toolCount)
	}
}

// A subagent the journal still shows running but no runtime is running was
// cut off by a restart; the list does not keep it running forever.
func TestSubagentListEndsStaleRunning(t *testing.T) {
	var seq atomic.Int64
	event := func(typ string, payload any) protocol.Event {
		e, err := protocol.NewEvent(seq.Add(1), "s", "run-1", typ, payload)
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	start := time.Now().UTC()
	events := []protocol.Event{
		event(protocol.EventToolCall, protocol.ToolCallEvent{ToolCall: protocol.ToolCall{ID: "call-1", Name: "subagent", Arguments: json.RawMessage(`{"prompt":"look around"}`)}}),
		event(protocol.EventSubagentStarted, protocol.SubagentEvent{Subagent: protocol.Subagent{ID: "foreground-9", Description: "look", Status: "running", ParentToolCallID: "call-1", StartedAt: &start}}),
		event(protocol.EventChildToolStart, map[string]any{"task_id": "foreground-9", "id": "c1", "name": "Read", "arguments": `{}`}),
		event("trajectory.request.completed", map[string]any{"task_id": "foreground-9", "usage": protocol.Usage{InputTokens: 10, OutputTokens: 4}}),
		event("trajectory.request.completed", map[string]any{"task_id": "foreground-9", "usage": protocol.Usage{InputTokens: 5, OutputTokens: 1}}),
	}
	list := foldSubagents(events, nil)
	if len(list) != 1 || list[0].Status != "running" || list[0].ToolCount != 1 || list[0].Usage == nil || list[0].Usage.InputTokens != 15 || list[0].Usage.OutputTokens != 5 {
		t.Fatalf("folded = %+v", list)
	}
	if got := childTranscript(events, "foreground-9", subagentPromptFromCall(events, "call-1"), protocol.ModelRef{}); len(got) != 2 || got[0].Content[0].Text != "look around" || len(got[1].ToolCalls) != 1 {
		t.Fatalf("transcript = %+v", got)
	}

	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fixture := newSubagentWorkflowFixture(t, "success")
	backend, server := newSubagentWorkflowServer(t, store, t.TempDir(), fixture.client)
	defer server.Close()
	sess := createTestSession(t, server.URL)
	backend.mu.Lock()
	rt := backend.sessions[sess.ID]
	backend.mu.Unlock()
	if rt == nil {
		t.Skip("session runtime not loaded after create")
	}
	for _, e := range events {
		if _, err := rt.publishTrajectory("stale-run", e.Type, json.RawMessage(e.Payload), ""); err != nil {
			t.Fatal(err)
		}
	}
	got := getSubagents(t, server.URL, sess.ID)
	if len(got.Subagents) != 1 || got.Subagents[0].Status != string(agent.TaskCancelled) {
		t.Fatalf("stale subagent = %+v", got)
	}
}

func TestRedactedTrajectoryHidesChildText(t *testing.T) {
	e, err := protocol.NewEvent(1, "s", "r", protocol.EventChildText, protocol.ChildTextEvent{TaskID: "sub-1", Text: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	out := redactTrajectoryEvents([]protocol.Event{e})
	var p protocol.ChildTextEvent
	if err := json.Unmarshal(out[0].Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.Text != "[redacted]" || p.TaskID != "sub-1" {
		t.Fatalf("redacted child text = %+v", p)
	}
}
