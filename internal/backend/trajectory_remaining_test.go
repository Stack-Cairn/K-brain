package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func TestTrajectoryRecordedRequestsSectionsTransportRetry(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "temporary provider failure", 503)
			return
		}
		writeSubagentWorkflowStream(w, "actual retry recovered")
	}))
	defer upstream.Close()
	client := ai.New(upstream.URL, "DO-NOT-LEAK-SECRET")
	client.MaxRetries = 2
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, server := newToolHistoryServer(t, store, t.TempDir(), client)
	sess := createTestSession(t, server.URL)
	accepted := runRequest(t, server.URL, sess.ID, "retry-record", "answer")
	readTrajectoryRun(t, server.URL, sess.ID, accepted, "allow_once")
	var window trajectoryWindow
	trajectoryGET(t, server.URL+"/v1/sessions/"+sess.ID+"/trajectory", 200, &window)
	starts, retries, transports := 0, 0, 0
	sectionID := ""
	for _, event := range window.RawEvents {
		p := trajectoryRequestPayload(event)
		switch event.Type {
		case "trajectory.request.started":
			starts++
			refs := p["sections"].([]any)
			sectionID = refs[0].(string)
		case "trajectory.retry":
			retries++
		case "trajectory.transport":
			transports++
			names := fmt.Sprint(p["header_names"])
			if !strings.Contains(names, "Authorization") {
				t.Fatalf("actual auth header name missing: %v", p)
			}
		}
	}
	if starts != 1 || retries != 1 || transports != 2 || calls.Load() != 2 {
		t.Fatalf("starts=%d retries=%d transport=%d calls=%d", starts, retries, transports, calls.Load())
	}
	data, _ := json.Marshal(window)
	if strings.Contains(string(data), "DO-NOT-LEAK-SECRET") {
		t.Fatal("transport leaked credentials")
	}
	var sections []trajectorySection
	trajectoryGET(t, server.URL+"/v1/sessions/"+sess.ID+"/trajectory/sections?section_id="+sectionID, 200, &sections)
	if len(sections) != 1 || sections[0].Content != "system" {
		t.Fatalf("actual prompt sections: %+v", sections)
	}
	if !strings.Contains(window.EventsJSON, `"k":"step_start"`) || !strings.Contains(window.EventsJSON, `"k":"retry"`) {
		t.Fatalf("missing UI facts: %s", window.EventsJSON)
	}
}

func TestTrajectoryHistoryBranchEditPrunesAndCopies(t *testing.T) {
	for _, bounded := range []bool{false, true} {
		t.Run(fmt.Sprintf("bounded=%v", bounded), func(t *testing.T) {

			store, err := session.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			backend, server := newTestServer(t, store, t.TempDir(), &scriptedClient{response: "answer"})
			sess := createTestSession(t, server.URL)
			for i := 0; i < 3; i++ {
				accepted := runRequest(t, server.URL, sess.ID, fmt.Sprint(i), fmt.Sprintf("turn %d", i))
				readTrajectoryRun(t, server.URL, sess.ID, accepted, "allow_once")
			}
			if bounded {
				rt, err := backend.loadRuntimeByID(sess.ID)
				if err != nil {
					t.Fatal(err)
				}
				for range maxCachedEvents + 1 {
					if _, err := backend.publish(rt, protocol.EventToolStatus, protocol.ToolStatus{Status: "running", Message: "tail"}, ""); err != nil {
						t.Fatal(err)
					}
				}
				sTruncated := rt.eventsTruncated
				backend.releaseRuntime(rt)
				if !sTruncated {
					t.Fatal("cache did not truncate")
				}
			}
			loaded := getSession(t, server.URL, sess.ID)
			users := []protocol.Message{}
			for _, m := range loaded.Messages {
				if m.Role == "user" {
					users = append(users, m)
				}
			}
			var branch protocol.Session
			postJSON(t, http.DefaultClient, server.URL+"/v1/sessions/"+sess.ID+"/branch", protocol.BranchSessionRequest{ExpectedRevision: loaded.Revision, MessageRef: protocol.HistoryMessageRef{MessageID: users[1].ID}}, &branch)
			var copied trajectoryWindow
			trajectoryGET(t, server.URL+"/v1/sessions/"+branch.ID+"/trajectory", 200, &copied)
			if copied.TotalSegmentCount != 2 || strings.Contains(copied.EventsJSON, "turn 2") {
				t.Fatalf("branch leaked later run: %+v", copied)
			}
			for _, event := range copied.RawEvents {
				if event.ConversationID != branch.ID {
					t.Fatal("branch event identity not rewritten")
				}
				if event.Type == "trajectory.request.started" {
					p := trajectoryRequestPayload(event)
					refs := p["sections"].([]any)
					var sections []trajectorySection
					trajectoryGET(t, server.URL+"/v1/sessions/"+branch.ID+"/trajectory/sections?section_id="+refs[0].(string), 200, &sections)
					if len(sections) != 1 {
						t.Fatal("branch section not copied")
					}
				}
			}
			var original trajectoryWindow
			trajectoryGET(t, server.URL+"/v1/sessions/"+sess.ID+"/trajectory", 200, &original)
			var edited protocol.Session
			response := postJSON(t, http.DefaultClient, server.URL+"/v1/sessions/"+sess.ID+"/edit", protocol.EditSessionRequest{ExpectedRevision: loaded.Revision, MessageRef: protocol.HistoryMessageRef{MessageID: users[1].ID}, Replacement: protocol.Message{Role: "user", Content: []protocol.ContentBlock{{Type: "text", Text: "replacement"}}}}, &edited)
			if response.StatusCode != 200 {
				t.Fatalf("edit: %d", response.StatusCode)
			}
			var retained trajectoryWindow
			trajectoryGET(t, server.URL+"/v1/sessions/"+sess.ID+"/trajectory", 200, &retained)
			if retained.TotalSegmentCount != 1 || retained.LastSeq <= original.LastSeq || retained.Truncated || strings.Contains(retained.EventsJSON, "turn 1") || strings.Contains(retained.EventsJSON, "turn 2") {
				t.Fatalf("edit prefix: %+v", retained)
			}
			backend.mu.Lock()
			delete(backend.sessions, sess.ID)
			backend.mu.Unlock()
			var cold trajectoryWindow
			trajectoryGET(t, server.URL+"/v1/sessions/"+sess.ID+"/trajectory", 200, &cold)
			if cold.LastSeq != retained.LastSeq || cold.Truncated {
				t.Fatal("restart lost rebase high watermark")
			}

		})
	}
}

func TestTrajectorySubagentDetailedActualRequest(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	fixture := newSubagentWorkflowFixture(t, "success")
	_, server := newSubagentWorkflowServer(t, store, t.TempDir(), fixture.client)
	sess := createTestSession(t, server.URL)
	accepted := runRequest(t, server.URL, sess.ID, "trajectory-child", "delegate")
	waitForSubagentTask(t, server.URL, sess.ID, "success")
	fixture.releaseParent()
	events := readSubagentWorkflowEvents(t, server.URL, sess.ID)
	assertSubagentWorkflowEvents(t, events, accepted.RunID, "success")
	loaded := getSession(t, server.URL, sess.ID)
	id := loaded.Tasks[0].ID
	var children []trajectoryChild
	trajectoryGET(t, server.URL+"/v1/sessions/"+sess.ID+"/trajectory/subagents?run_id="+url.QueryEscape(id), 200, &children)
	if len(children) != 1 || len(children[0].Steps) != 1 || children[0].Steps[0].StartedAt == nil || children[0].Steps[0].EndedAt == nil || children[0].Status != "complete" {
		t.Fatalf("actual child details: %+v", children)
	}
	var window trajectoryWindow
	trajectoryGET(t, server.URL+"/v1/sessions/"+sess.ID+"/trajectory", 200, &window)
	if !strings.Contains(window.EventsJSON, id) {
		t.Fatalf("parent tool lacks child run ref: %s", window.EventsJSON)
	}
}

func TestTrajectoryForegroundChildActualTool(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request ai.Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		last := request.Messages[len(request.Messages)-1]
		if last.Role == "tool" {
			writeSubagentWorkflowStream(w, "child tool handled")
			return
		}
		if isSubagentRequest(request) {
			w.Header().Set("Content-Type", "text/event-stream")
			args, _ := json.Marshal(map[string]string{"command": "printf 'actual-child-tool-output\\n'"})
			writeToolHistorySSE(w, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "child-bash", "type": "function", "function": map[string]any{"name": "bash", "arguments": string(args)}}}}, "finish_reason": "tool_calls"}}})
			fmt.Fprint(w, "data: [DONE]\n\n")
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
	accepted := runRequest(t, server.URL, sess.ID, "foreground-child", "delegate tool")
	events := readTrajectoryRun(t, server.URL, sess.ID, accepted, "allow_once")
	children := buildTrajectorySubagents(events)
	if len(children) != 1 {
		t.Fatalf("children: %+v", children)
	}
	for id, child := range children {
		if child.Status != "complete" || len(child.Steps) != 2 || len(child.Steps[0].Tools) != 1 {
			t.Fatalf("child %+v", child)
		}
		tool := child.Steps[0].Tools[0]
		if tool.StartedAt == nil || tool.EndedAt == nil || tool.Name != "bash" || tool.IsError {
			t.Fatalf("actual tool: %+v", tool)
		}
		var details []trajectoryChild
		trajectoryGET(t, server.URL+"/v1/sessions/"+sess.ID+"/trajectory/subagents?run_id="+url.QueryEscape(id), 200, &details)
		if len(details) != 1 || len(details[0].Steps[0].Tools) != 1 {
			t.Fatal("HTTP child tool detail missing")
		}
		if evidence := os.Getenv("KBRAIN_TRAJECTORY_EVIDENCE"); evidence != "" {
			var window trajectoryWindow
			trajectoryGET(t, server.URL+"/v1/sessions/"+sess.ID+"/trajectory", 200, &window)
			data, _ := json.MarshalIndent(map[string]any{"conversationId": sess.ID, "runId": id, "window": window}, "", "  ")
			if err := os.WriteFile(filepath.Join(evidence, "trajectory-child-runtime.json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			if gui := os.Getenv("KBRAIN_TRAJECTORY_GUI_DIR"); gui != "" {
				cmd := exec.Command("node", "--test", "--test-name-pattern=actual child", "test/trajectory/kbrain-trajectory.test.mjs")
				cmd.Dir = gui
				cmd.Env = append(os.Environ(), "KBRAIN_TRAJECTORY_CHILD_URL="+server.URL)
				output, err := cmd.CombinedOutput()
				t.Logf("child GUI HTTP: %s", output)
				if err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}
