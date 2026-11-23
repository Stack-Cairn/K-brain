package backend

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func cancelRun(t *testing.T, base, sessionID, runID string) {
	t.Helper()
	resp, err := http.Post(base+"/v1/sessions/"+sessionID+"/runs/"+runID+"/cancel", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cancel status %d", resp.StatusCode)
	}
}

func waitStreamed(t *testing.T, streamed chan struct{}) {
	t.Helper()
	select {
	case <-streamed:
	case <-time.After(10 * time.Second):
		t.Fatal("model request did not stream")
	}
}

func TestServerCancelledRunPersistsStreamedReply(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	eventDir := t.TempDir()
	fixture := &scriptedClient{wait: true, partial: "Half of the answer", streamed: make(chan struct{}, 1)}
	backendServer, httpServer := newTestServer(t, store, eventDir, fixture)
	sess := createTestSession(t, httpServer.URL)

	accepted := runRequest(t, httpServer.URL, sess.ID, "cancel-partial", "explain")
	waitStreamed(t, fixture.streamed)
	cancelRun(t, httpServer.URL, sess.ID, accepted.RunID)
	events := waitForRun(t, httpServer.URL, sess.ID, 0)
	if len(events) == 0 || events[len(events)-1].Type != protocol.EventRunCancelled {
		t.Fatalf("cancel events = %+v", events)
	}
	// Released desktop builds merge an assistant.message.created into the streamed
	// copy; history is where the stopped reply is read from.
	for _, event := range events {
		if event.Type == protocol.EventAssistantMessage {
			t.Fatalf("cancel announced the reply as an event: %+v", event)
		}
	}

	var page historyResponse
	if code := doJSON(t, http.MethodGet, httpServer.URL+"/v1/sessions/"+sess.ID+"/history?max_messages=100", nil, &page); code != http.StatusOK {
		t.Fatalf("history status %d", code)
	}
	messages := page.Session.Messages
	if len(messages) != 2 || messages[0].Role != protocol.RoleUser || messages[1].Role != protocol.RoleAssistant || messages[1].ID == "" || messages[1].StopReason != "cancelled" || len(messages[1].Content) != 1 || messages[1].Content[0].Text != "Half of the answer" {
		t.Fatalf("history after cancel = %+v", messages)
	}

	// The reply survives a restart and the next run builds a valid request on it.
	if err := backendServer.Close(); err != nil {
		t.Fatal(err)
	}
	httpServer.Close()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store2, err := session.Open(store.SessionsDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	next := &scriptedClient{response: "full answer"}
	_, httpServer2 := newTestServer(t, store2, eventDir, next)
	recovered := getSession(t, httpServer2.URL, sess.ID)
	if len(recovered.Messages) != 2 || recovered.Messages[1].StopReason != "cancelled" {
		t.Fatalf("recovered history = %+v", recovered.Messages)
	}
	runRequest(t, httpServer2.URL, sess.ID, "after-cancel", "go on")
	if after := waitForRun(t, httpServer2.URL, sess.ID, recovered.LastSeq); after[len(after)-1].Type != protocol.EventRunCompleted {
		t.Fatalf("next run events = %+v", after)
	}
	next.mu.Lock()
	request := next.requests[0]
	next.mu.Unlock()
	if len(request) != 4 || request[2].Role != protocol.RoleAssistant || request[2].Content != "Half of the answer" || request[2].StopReason != ai.StopReasonCancelled || request[3].Content != "go on" {
		t.Fatalf("next request history = %+v", request)
	}
	final := getSession(t, httpServer2.URL, sess.ID)
	if len(final.Messages) != 4 || final.Messages[3].Content[0].Text != "full answer" {
		t.Fatalf("final history = %+v", final.Messages)
	}
}

// Stopping a run before the model wrote anything saves an empty
// "assistant-stopped-<run>" reply with stop_reason "cancelled", so the stop
// is still visible after reopening the conversation.
func TestServerCancelBeforeOutputPersistsStoppedMarker(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fixture := &scriptedClient{wait: true, streamed: make(chan struct{}, 1)}
	_, httpServer := newTestServer(t, store, t.TempDir(), fixture)
	sess := createTestSession(t, httpServer.URL)

	accepted := runRequest(t, httpServer.URL, sess.ID, "cancel-empty", "explain")
	waitStreamed(t, fixture.streamed)
	cancelRun(t, httpServer.URL, sess.ID, accepted.RunID)
	events := waitForRun(t, httpServer.URL, sess.ID, 0)
	if events[len(events)-1].Type != protocol.EventRunCancelled {
		t.Fatalf("cancel events = %+v", events)
	}
	for _, event := range events {
		if event.Type == protocol.EventAssistantMessage {
			t.Fatalf("cancel before output announced a reply: %+v", event)
		}
	}
	var page historyResponse
	if code := doJSON(t, http.MethodGet, httpServer.URL+"/v1/sessions/"+sess.ID+"/history?max_messages=100", nil, &page); code != http.StatusOK {
		t.Fatalf("history status %d", code)
	}
	messages := page.Session.Messages
	if len(messages) != 2 || messages[0].Role != protocol.RoleUser {
		t.Fatalf("history after empty cancel = %+v", messages)
	}
	if marker := messages[1]; marker.ID != "assistant-stopped-"+accepted.RunID || marker.Role != protocol.RoleAssistant || marker.StopReason != "cancelled" || len(marker.Content) != 0 || marker.CancelReason != "" || marker.CreatedAt == nil {
		t.Fatalf("stopped marker = %+v", marker)
	}
	var terminal protocol.RunTerminal
	if err := json.Unmarshal(events[len(events)-1].Payload, &terminal); err != nil || terminal.State != "cancelled" || terminal.CancelReason != "" {
		t.Fatalf("run.cancelled = %s (%v)", events[len(events)-1].Payload, err)
	}
}

// A prompt stopped before any reply can still be resumed by its message id;
// the reply takes the stop marker's place in history.
func TestServerResumeReplacesStoppedMarker(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fixture := &scriptedClient{wait: true, streamed: make(chan struct{}, 1), response: "resumed answer"}
	_, httpServer := newTestServer(t, store, t.TempDir(), fixture)
	sess := createTestSession(t, httpServer.URL)
	accepted := runRequest(t, httpServer.URL, sess.ID, "stop-early", "explain")
	waitStreamed(t, fixture.streamed)
	cancelRun(t, httpServer.URL, sess.ID, accepted.RunID)
	waitForRun(t, httpServer.URL, sess.ID, 0)
	stopped := getSession(t, httpServer.URL, sess.ID)
	if len(stopped.Messages) != 2 || stopped.Messages[1].ID != "assistant-stopped-"+accepted.RunID {
		t.Fatalf("history after stop = %+v", stopped.Messages)
	}
	fixture.mu.Lock()
	fixture.wait = false
	fixture.mu.Unlock()
	prompt := stopped.Messages[0]
	var resumed protocol.RunAccepted
	if resp := postJSON(t, http.DefaultClient, httpServer.URL+"/v1/sessions/"+sess.ID+"/runs", protocol.PromptRequest{ConversationID: sess.ID, ClientRequestID: "resume", ResumeMessageID: prompt.ID, Prompt: "explain"}, &resumed); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("resume status %d", resp.StatusCode)
	}
	if events := waitForRun(t, httpServer.URL, sess.ID, stopped.LastSeq); events[len(events)-1].Type != protocol.EventRunCompleted {
		t.Fatalf("resume events = %+v", events)
	}
	_, saved, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, message := range saved {
		if message.Role != "system" {
			texts = append(texts, message.Role+":"+message.TextContent())
		}
	}
	if len(texts) != 2 || texts[0] != "user:explain" || texts[1] != "assistant:resumed answer" {
		t.Fatalf("saved history after resume = %v", texts)
	}
}
