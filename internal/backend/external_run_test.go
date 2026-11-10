package backend

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func getJSON(t *testing.T, url string, out any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET %s = %d: %s", url, resp.StatusCode, body)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatal(err)
	}
}

// The kn terminal UI shares the backend's session directory. While it runs a
// turn the backend reports the run and refuses its own; afterwards it continues
// from kn's messages instead of overwriting them from its cached runtime.
func TestBackendHonoursRunsInAnotherProcess(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fixture := &scriptedClient{response: "from backend"}
	_, httpServer := newTestServer(t, store, t.TempDir(), fixture)
	base := httpServer.URL
	sess := createTestSession(t, base)
	first := runRequest(t, base, sess.ID, "first", "first prompt")
	waitForRunTerminal(t, base, sess.ID, first.RunID)

	// A second Store on the same directory stands in for the kn process.
	kn, err := session.Open(store.SessionsDir())
	if err != nil {
		t.Fatal(err)
	}
	defer kn.Close()
	lease, err := kn.AcquireRun(sess.ID, "kn")
	if err != nil {
		t.Fatal(err)
	}
	resp := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+sess.ID+"/runs", protocol.PromptRequest{
		ConversationID: sess.ID, ClientRequestID: "while-kn-runs", Prompt: "collide",
	}, nil)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "running in kn") {
		t.Fatalf("run while kn runs = %d %s", resp.StatusCode, body)
	}
	var view protocol.Session
	getJSON(t, base+"/v1/sessions/"+sess.ID, &view)
	if view.ExternalRun == nil || view.ExternalRun.Client != "kn" || view.ExternalRun.StartedAt.IsZero() {
		t.Fatalf("session view external_run = %+v", view.ExternalRun)
	}
	var page protocol.SessionPage
	getJSON(t, base+"/v1/sessions?limit=10", &page)
	if len(page.Sessions) != 1 || page.Sessions[0].ExternalRun == nil || page.Sessions[0].ExternalRun.Client != "kn" {
		t.Fatalf("list = %+v", page.Sessions)
	}

	// kn's turn appends to the transcript the backend has cached.
	history, msgs, err := kn.History(sess.ID, []ai.Message{{Role: "system", Content: "system"}})
	if err != nil {
		t.Fatal(err)
	}
	msgs = append(msgs, ai.Message{Role: "user", Content: "kn prompt", Authored: true}, ai.Message{Role: "assistant", Content: "kn answer", StopReason: ai.StopReasonStop})
	if err := kn.SaveHistory(sess.ID, history, msgs, "fixture-model", "fixture"); err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}

	page = protocol.SessionPage{}
	getJSON(t, base+"/v1/sessions?limit=10", &page)
	if page.Sessions[0].ExternalRun != nil {
		t.Fatalf("finished kn run still reported: %+v", page.Sessions[0].ExternalRun)
	}
	second := runRequest(t, base, sess.ID, "second", "second prompt")
	waitForRunTerminal(t, base, sess.ID, second.RunID)
	view = protocol.Session{}
	getJSON(t, base+"/v1/sessions/"+sess.ID, &view)
	var texts []string
	for _, message := range view.Messages {
		for _, block := range message.Content {
			texts = append(texts, message.Role+":"+block.Text)
		}
	}
	want := []string{"user:first prompt", "assistant:from backend", "user:kn prompt", "assistant:kn answer", "user:second prompt", "assistant:from backend"}
	if strings.Join(texts, "|") != strings.Join(want, "|") {
		t.Fatalf("messages = %q, want %q", texts, want)
	}
	fixture.mu.Lock()
	last := fixture.requests[len(fixture.requests)-1]
	fixture.mu.Unlock()
	sawKN := false
	for _, message := range last {
		sawKN = sawKN || message.TextContent() == "kn answer"
	}
	if !sawKN {
		t.Fatal("the backend's next run did not see kn's turn")
	}

	// While the backend runs, kn is the one refused.
	fixture.mu.Lock()
	fixture.wait = true
	fixture.mu.Unlock()
	third := runRequest(t, base, sess.ID, "third", "third prompt")
	if _, err := kn.AcquireRun(sess.ID, "kn"); err == nil || !strings.Contains(err.Error(), "running in backend") {
		t.Fatalf("kn lease while the backend runs = %v", err)
	}
	cancel := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+sess.ID+"/runs/"+third.RunID+"/cancel", map[string]any{}, nil)
	_ = cancel.Body.Close()
	waitForRunTerminal(t, base, sess.ID, third.RunID)
	again, err := kn.AcquireRun(sess.ID, "kn")
	if err != nil {
		t.Fatalf("kn lease after the backend run = %v", err)
	}
	_ = again.Release()
}

// Subagents a kn turn starts are saved in the shared store while they run. The
// backend lists them as running elsewhere, serves the saved transcript, keeps
// them running when it loads the session, and reports them cut off once kn's
// process is gone.
func TestBackendServesSubagentsRunningInAnotherProcess(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	srv, httpServer := newTestServer(t, store, t.TempDir(), &scriptedClient{response: "from backend"})
	base := httpServer.URL
	sess := createTestSession(t, base)

	kn, err := session.Open(store.SessionsDir())
	if err != nil {
		t.Fatal(err)
	}
	defer kn.Close()
	token, err := kn.TaskLease(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	usage := ai.Usage{PromptTokens: 12, CompletionTokens: 5}
	started := time.Now().Add(-time.Second)
	for _, task := range []session.Task{
		{ID: "foreground-1", Description: "Check the build", Prompt: "check it", Status: "running", StartedAt: started, ParentToolCallID: "call-fg", Foreground: true, ToolCount: 2, Usage: &usage, Lease: token, Model: "fixture-model"},
		{ID: "survey-2", Description: "Survey docs", Prompt: "survey", Status: "running", StartedAt: started.Add(time.Millisecond), ParentToolCallID: "call-bg", Lease: token},
	} {
		if err := kn.SaveTask(sess.ID, task); err != nil {
			t.Fatal(err)
		}
	}
	child := []ai.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "check it"}, {Role: "assistant", Content: "looking"}}
	if _, err := kn.SaveSubagentTranscript(sess.ID, "foreground-1", child, "fixture-model", ""); err != nil {
		t.Fatal(err)
	}

	var list protocol.SubagentList
	getJSON(t, base+"/v1/sessions/"+sess.ID+"/subagents", &list)
	if len(list.Subagents) != 2 {
		t.Fatalf("subagents = %+v", list.Subagents)
	}
	fg, bg := list.Subagents[0], list.Subagents[1]
	if fg.ID != "foreground-1" || fg.Status != "running" || !fg.External || fg.Background || fg.ToolCount != 2 || fg.Usage == nil || fg.Usage.InputTokens != 12 || fg.ParentToolCallID != "call-fg" {
		t.Fatalf("foreground view = %+v", fg)
	}
	if bg.ID != "survey-2" || bg.Status != "running" || !bg.External || !bg.Background {
		t.Fatalf("background view = %+v", bg)
	}
	var transcript protocol.SubagentTranscript
	getJSON(t, base+"/v1/sessions/"+sess.ID+"/subagents/foreground-1", &transcript)
	if transcript.Live || !transcript.Subagent.External || len(transcript.Messages) != 2 || transcript.Messages[1].Role != protocol.RoleAssistant {
		t.Fatalf("transcript = live %v %+v", transcript.Live, transcript.Messages)
	}
	transcript = protocol.SubagentTranscript{}
	getJSON(t, base+"/v1/sessions/"+sess.ID+"/subagents/survey-2", &transcript)
	if len(transcript.Messages) != 1 || transcript.Messages[0].Content[0].Text != "survey" {
		t.Fatalf("unsaved transcript falls back to the prompt: %+v", transcript.Messages)
	}
	resp := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+sess.ID+"/subagents/survey-2/cancel", map[string]any{}, nil)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "another process") {
		t.Fatalf("cancel = %d %s", resp.StatusCode, body)
	}

	// Loading the session in the backend must not mark kn's subagents stale.
	if err := srv.reconcileStaleTasks(sess.ID); err != nil {
		t.Fatal(err)
	}
	tasks, err := store.LoadTasks(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		if task.Status != "running" {
			t.Fatalf("backend rewrote kn's running subagent: %+v", task)
		}
	}

	if err := kn.Close(); err != nil {
		t.Fatal(err)
	}
	list = protocol.SubagentList{}
	getJSON(t, base+"/v1/sessions/"+sess.ID+"/subagents", &list)
	for _, view := range list.Subagents {
		if view.Status != "cancelled" || view.External {
			t.Fatalf("subagent of an exited process = %+v", view)
		}
	}
}
