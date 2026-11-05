package backend

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

type scriptedClient struct {
	mu        sync.Mutex
	calls     int
	models    []string
	requests  [][]ai.Message
	response  string
	streamErr error
	wait      bool
	// partial streams before a waiting response blocks, as if the model had
	// started its reply when the user stopped it.
	partial string
	// streamed, when set, receives one value per request once partial is out.
	streamed chan struct{}
}

func (c *scriptedClient) Models(context.Context) ([]ai.ModelInfo, error) { return nil, nil }
func (c *scriptedClient) Complete(_ context.Context, request ai.Request) (string, ai.Usage, error) {
	c.mu.Lock()
	c.calls++
	c.models = append(c.models, request.Model)
	c.mu.Unlock()
	return c.response, ai.Usage{}, nil
}
func (c *scriptedClient) Clone() ai.Client   { return c }
func (c *scriptedClient) SetCacheKey(string) {}
func (c *scriptedClient) Endpoint() string   { return "http://fixture.invalid" }
func (c *scriptedClient) Stream(ctx context.Context, request ai.Request, onText, onThink func(string), _ func(string, string, string)) (ai.Message, ai.Usage, error) {
	c.mu.Lock()
	c.calls++
	c.models = append(c.models, request.Model)
	c.requests = append(c.requests, append([]ai.Message(nil), request.Messages...))
	wait := c.wait
	response := c.response
	streamErr := c.streamErr
	partial := c.partial
	streamed := c.streamed
	c.mu.Unlock()
	if wait {
		if partial != "" && onText != nil {
			onText(partial)
		}
		if streamed != nil {
			streamed <- struct{}{}
		}
		<-ctx.Done()
		return ai.Message{}, ai.Usage{}, ctx.Err()
	}
	if streamErr != nil {
		return ai.Message{}, ai.Usage{}, streamErr
	}
	if onThink != nil {
		onThink("thinking")
	}
	if onText != nil {
		onText(response)
	}
	return ai.Message{Role: "assistant", Content: response, StopReason: ai.StopReasonStop}, ai.Usage{PromptTokens: 3, CompletionTokens: 2}, nil
}

func newTestServer(t *testing.T, store *session.Store, eventDir string, client *scriptedClient) (*Server, *httptest.Server) {
	t.Helper()
	factory := func(_ context.Context, _ string, selected protocol.ModelRef) (*agent.Agent, error) {
		ag := agent.New(client, selected.Model, 1024, "system")
		ag.ModelName = selected.Model
		ag.Provider = selected.Provider
		return ag, nil
	}
	backendServer, err := New(Options{
		Store:      store,
		Factory:    factory,
		EventDir:   eventDir,
		Models:     []protocol.ModelRef{{Provider: "fixture", Model: "fixture-model"}},
		DefaultCWD: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(backendServer)
	t.Cleanup(func() {
		httpServer.Close()
		_ = backendServer.Close()
	})
	return backendServer, httpServer
}

func postJSON(t *testing.T, client *http.Client, url string, in any, out any) *http.Response {
	t.Helper()
	body, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if out != nil {
		defer resp.Body.Close()
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
	return resp
}

func createTestSession(t *testing.T, base string) protocol.Session {
	t.Helper()
	resp := postJSON(t, http.DefaultClient, base+"/v1/sessions", protocol.CreateSessionRequest{
		CWD:   t.TempDir(),
		Model: protocol.ModelRef{Provider: "fixture", Model: "fixture-model"},
	}, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("create status %d: %s", resp.StatusCode, b)
	}
	var out protocol.Session
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func readSSE(t *testing.T, resp *http.Response) []protocol.Event {
	t.Helper()
	defer resp.Body.Close()
	var events []protocol.Event
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event protocol.Event
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return events
}

func runRequest(t *testing.T, base, sessionID, requestID, prompt string) protocol.RunAccepted {
	t.Helper()
	var out protocol.RunAccepted
	resp := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+sessionID+"/runs", protocol.PromptRequest{
		ConversationID: sessionID, ClientRequestID: requestID, Prompt: prompt,
	}, &out)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("run status %d", resp.StatusCode)
	}
	return out
}

func TestServerCanonicalRunSSEReplayAndRecovery(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	eventDir := t.TempDir()
	fixture := &scriptedClient{response: "hello from fixture"}
	_, httpServer := newTestServer(t, store, eventDir, fixture)

	health, err := http.Get(httpServer.URL + "/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	if health.StatusCode != http.StatusOK {
		t.Fatalf("health status %d", health.StatusCode)
	}
	_ = health.Body.Close()

	sess := createTestSession(t, httpServer.URL)
	accepted := runRequest(t, httpServer.URL, sess.ID, "req-1", "say hello")
	if accepted.RunID == "" || accepted.AcceptedSeq < 1 {
		t.Fatalf("accepted = %+v", accepted)
	}

	resp, err := http.Get(httpServer.URL + "/v1/sessions/" + sess.ID + "/events?after_seq=0")
	if err != nil {
		t.Fatal(err)
	}
	events := readSSE(t, resp)
	if len(events) < 5 {
		t.Fatalf("events = %+v", events)
	}
	var sawUser, sawText, sawAssistant, sawCompleted bool
	var lastSeq int64
	for _, event := range events {
		if event.Seq <= lastSeq || event.RunID != accepted.RunID {
			t.Fatalf("event ordering or identity invalid: previous=%d event=%+v", lastSeq, event)
		}
		lastSeq = event.Seq
		switch event.Type {
		case protocol.EventUserMessage:
			sawUser = true
		case protocol.EventTextDelta:
			sawText = true
		case protocol.EventAssistantMessage:
			var message protocol.Message
			if err := json.Unmarshal(event.Payload, &message); err != nil {
				t.Fatal(err)
			}
			if len(message.Content) == 0 || message.Content[0].Text != "hello from fixture" {
				t.Fatalf("assistant message = %+v", message)
			}
			sawAssistant = true
		case protocol.EventRunCompleted:
			sawCompleted = true
		}
	}
	if !sawUser || !sawText || !sawAssistant || !sawCompleted {
		t.Fatalf("missing canonical events: user=%v text=%v assistant=%v completed=%v", sawUser, sawText, sawAssistant, sawCompleted)
	}

	var duplicate protocol.RunAccepted
	resp = postJSON(t, http.DefaultClient, httpServer.URL+"/v1/sessions/"+sess.ID+"/runs", protocol.PromptRequest{
		ConversationID: sess.ID, ClientRequestID: "req-1", Prompt: "say hello",
	}, &duplicate)
	if resp.StatusCode != http.StatusOK || duplicate.RunID != accepted.RunID {
		t.Fatalf("idempotent response status=%d body=%+v", resp.StatusCode, duplicate)
	}

	loadedResp, err := http.Get(httpServer.URL + "/v1/sessions/" + sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	var loaded protocol.Session
	if err := json.NewDecoder(loadedResp.Body).Decode(&loaded); err != nil {
		t.Fatal(err)
	}
	_ = loadedResp.Body.Close()
	if loaded.MessageCount < 2 || loaded.LastSeq != lastSeq {
		t.Fatalf("loaded session = %+v", loaded)
	}

	// A new backend instance must load the persisted messages and event journal.
	store2, err := session.Open(store.SessionsDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	fixture2 := &scriptedClient{response: "second backend"}
	_, httpServer2 := newTestServer(t, store2, eventDir, fixture2)
	resp, err = http.Get(httpServer2.URL + "/v1/sessions/" + sess.ID + "/events?after_seq=0")
	if err != nil {
		t.Fatal(err)
	}
	replayed := readSSE(t, resp)
	if len(replayed) != len(events) || replayed[len(replayed)-1].Seq != lastSeq {
		t.Fatalf("replayed events = %d/%d, last=%+v", len(replayed), len(events), replayed[len(replayed)-1])
	}
}

func patchSession(t *testing.T, base, id, body string, out any) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPatch, base+"/v1/sessions/"+id, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	} else {
		_, _ = io.Copy(io.Discard, resp.Body)
	}
	return resp.StatusCode
}

func getSession(t *testing.T, base, id string) protocol.Session {
	t.Helper()
	resp, err := http.Get(base + "/v1/sessions/" + id)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("get session status %d: %s", resp.StatusCode, b)
	}
	var out protocol.Session
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func waitForRun(t *testing.T, base, id string, after int64) []protocol.Event {
	t.Helper()
	resp, err := http.Get(base + "/v1/sessions/" + id + "/events?after_seq=" + strconv.FormatInt(after, 10))
	if err != nil {
		t.Fatal(err)
	}
	return readSSE(t, resp)
}

func (c *scriptedClient) requestedModels() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.models...)
}

func TestServerPersistsFailedAssistantAcrossReloadAndContinues(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	eventDir := t.TempDir()
	fixture := &scriptedClient{streamErr: errors.New("upstream request failed")}
	backendServer, httpServer := newTestServer(t, store, eventDir, fixture)
	sess := createTestSession(t, httpServer.URL)

	accepted := runRequest(t, httpServer.URL, sess.ID, "failed-1", "use native search")
	events := waitForRun(t, httpServer.URL, sess.ID, 0)
	var failedAssistant *protocol.Message
	var sawFailed bool
	for _, event := range events {
		switch event.Type {
		case protocol.EventAssistantMessage:
			var message protocol.Message
			if err := json.Unmarshal(event.Payload, &message); err != nil {
				t.Fatal(err)
			}
			failedAssistant = &message
		case protocol.EventRunFailed:
			var terminal protocol.RunTerminal
			if err := json.Unmarshal(event.Payload, &terminal); err != nil {
				t.Fatal(err)
			}
			if terminal.Error != "upstream request failed" {
				t.Fatalf("failure terminal = %+v", terminal)
			}
			sawFailed = true
		}
	}
	if accepted.RunID == "" || failedAssistant == nil || !sawFailed {
		t.Fatalf("failure events = %+v", events)
	}
	if failedAssistant.Role != protocol.RoleAssistant || failedAssistant.StopReason != string(ai.StopReasonError) || len(failedAssistant.Content) != 1 || failedAssistant.Content[0].Text != "upstream request failed" {
		t.Fatalf("failed assistant = %+v", failedAssistant)
	}
	failedHistory := getSession(t, httpServer.URL, sess.ID)
	if len(failedHistory.Messages) != 2 || failedHistory.Messages[1].Role != protocol.RoleAssistant || failedHistory.Messages[1].StopReason != string(ai.StopReasonError) {
		t.Fatalf("failed history = %+v", failedHistory.Messages)
	}

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
	recoveredClient := &scriptedClient{response: "recovered success"}
	_, httpServer2 := newTestServer(t, store2, eventDir, recoveredClient)
	recovered := getSession(t, httpServer2.URL, sess.ID)
	if len(recovered.Messages) != 2 || recovered.Messages[1].Content[0].Text != "upstream request failed" {
		t.Fatalf("recovered history = %+v", recovered.Messages)
	}

	before := recovered.LastSeq
	runRequest(t, httpServer2.URL, sess.ID, "success-1", "try again")
	successEvents := waitForRun(t, httpServer2.URL, sess.ID, before)
	if len(successEvents) == 0 || successEvents[len(successEvents)-1].Type != protocol.EventRunCompleted {
		t.Fatalf("success events = %+v", successEvents)
	}
	successHistory := getSession(t, httpServer2.URL, sess.ID)
	if len(successHistory.Messages) != 4 || successHistory.Messages[2].Content[0].Text != "try again" || successHistory.Messages[3].Content[0].Text != "recovered success" {
		t.Fatalf("success history = %+v", successHistory.Messages)
	}
	recoveredClient.mu.Lock()
	if len(recoveredClient.requests) != 1 || len(recoveredClient.requests[0]) < 3 || recoveredClient.requests[0][len(recoveredClient.requests[0])-2].Role != protocol.RoleAssistant {
		recoveredClient.mu.Unlock()
		t.Fatalf("recovered provider request history = %+v", recoveredClient.requests)
	}
	recoveredClient.mu.Unlock()
}

func TestServerUpdatesSessionModelAndPersistsIt(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	eventDir := t.TempDir()
	fixture := &scriptedClient{response: "model switched"}
	backendServer, httpServer := newTestServer(t, store, eventDir, fixture)
	sess := createTestSession(t, httpServer.URL)

	runRequest(t, httpServer.URL, sess.ID, "model-1", "say hello")
	events := waitForRun(t, httpServer.URL, sess.ID, 0)
	if len(events) == 0 || events[len(events)-1].Type != protocol.EventRunCompleted {
		t.Fatalf("initial run events = %+v", events)
	}
	beforePatch := getSession(t, httpServer.URL, sess.ID)
	if beforePatch.MessageCount != 2 || len(beforePatch.Messages) != 2 || beforePatch.Messages[0].Role != protocol.RoleUser || beforePatch.Messages[0].Content[0].Text != "say hello" || beforePatch.Messages[1].Content[0].Text != "model switched" {
		t.Fatalf("initial history = %+v", beforePatch.Messages)
	}
	if got := fixture.requestedModels(); len(got) != 1 || got[0] != "fixture-model" {
		t.Fatalf("initial request models = %v", got)
	}

	const selectedModel = "fixture-model-new"
	selected := protocol.ModelRef{Provider: "fixture-new", Model: selectedModel}
	var updated protocol.Session
	body, err := json.Marshal(protocol.UpdateSessionRequest{Model: &selected})
	if err != nil {
		t.Fatal(err)
	}
	if status := patchSession(t, httpServer.URL, sess.ID, string(body), &updated); status != http.StatusOK {
		t.Fatalf("patch status = %d", status)
	}
	if updated.Model != selected || updated.MessageCount != beforePatch.MessageCount || len(updated.Messages) != len(beforePatch.Messages) {
		t.Fatalf("updated session = %+v", updated)
	}
	if updated.Messages[0].Content[0].Text != beforePatch.Messages[0].Content[0].Text || updated.Messages[1].Content[0].Text != beforePatch.Messages[1].Content[0].Text {
		t.Fatalf("patch changed history: before=%+v after=%+v", beforePatch.Messages, updated.Messages)
	}

	beforeSecond := getSession(t, httpServer.URL, sess.ID)
	second := runRequest(t, httpServer.URL, sess.ID, "model-2", "continue")
	secondEvents := waitForRun(t, httpServer.URL, sess.ID, beforeSecond.LastSeq)
	if len(secondEvents) == 0 || secondEvents[len(secondEvents)-1].Type != protocol.EventRunCompleted || second.RunID != secondEvents[0].RunID {
		t.Fatalf("second run = %+v, events = %+v", second, secondEvents)
	}
	if got := fixture.requestedModels(); len(got) != 2 || got[1] != selectedModel {
		t.Fatalf("post-patch request models = %v", got)
	}
	beforeRestart := getSession(t, httpServer.URL, sess.ID)
	if beforeRestart.MessageCount != 4 || len(beforeRestart.Messages) != 4 || beforeRestart.Messages[2].Content[0].Text != "continue" {
		t.Fatalf("history after second run = %+v", beforeRestart.Messages)
	}

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
	fixture2 := &scriptedClient{response: "after restart"}
	_, httpServer2 := newTestServer(t, store2, eventDir, fixture2)
	recovered := getSession(t, httpServer2.URL, sess.ID)
	if recovered.Model != selected || recovered.MessageCount != beforeRestart.MessageCount || len(recovered.Messages) != 4 {
		t.Fatalf("recovered session = %+v", recovered)
	}
	if recovered.Messages[0].Content[0].Text != "say hello" || recovered.Messages[1].Content[0].Text != "model switched" || recovered.Messages[2].Content[0].Text != "continue" {
		t.Fatalf("recovered history = %+v", recovered.Messages)
	}

	runRequest(t, httpServer2.URL, sess.ID, "model-3", "after restart")
	thirdEvents := waitForRun(t, httpServer2.URL, sess.ID, recovered.LastSeq)
	if len(thirdEvents) == 0 || thirdEvents[len(thirdEvents)-1].Type != protocol.EventRunCompleted {
		t.Fatalf("third run events = %+v", thirdEvents)
	}
	if got := fixture2.requestedModels(); len(got) != 1 || got[0] != selectedModel {
		t.Fatalf("post-restart request models = %v", got)
	}
}

func TestServerRejectsInvalidOrActiveSessionModelUpdates(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fixture := &scriptedClient{wait: true}
	_, httpServer := newTestServer(t, store, t.TempDir(), fixture)
	sess := createTestSession(t, httpServer.URL)

	for _, body := range []string{"{}", `{"model":null}`, `{"model":{}}`, `{"model":{"provider":"fixture"}}`, `{"model":{"model":"   "}}`, "", "{"} {
		if status := patchSession(t, httpServer.URL, sess.ID, body, nil); status != http.StatusBadRequest {
			t.Fatalf("invalid patch %q status = %d", body, status)
		}
	}
	accepted := runRequest(t, httpServer.URL, sess.ID, "active-model", "wait")
	body := `{"model":{"provider":"fixture-new","model":"fixture-model-new"}}`
	if status := patchSession(t, httpServer.URL, sess.ID, body, nil); status != http.StatusConflict {
		t.Fatalf("active patch status = %d", status)
	}
	cancelResp, err := http.Post(httpServer.URL+"/v1/sessions/"+sess.ID+"/runs/"+accepted.RunID+"/cancel", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	if cancelResp.StatusCode != http.StatusOK {
		t.Fatalf("cancel status = %d", cancelResp.StatusCode)
	}
	cancelResp.Body.Close()
	_ = waitForRun(t, httpServer.URL, sess.ID, 0)
}

func TestServerCancelRunPublishesTerminalEvent(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fixture := &scriptedClient{wait: true}
	_, httpServer := newTestServer(t, store, t.TempDir(), fixture)
	sess := createTestSession(t, httpServer.URL)
	accepted := runRequest(t, httpServer.URL, sess.ID, "cancel-1", "wait")

	resp, err := http.Post(httpServer.URL+"/v1/sessions/"+sess.ID+"/runs/"+accepted.RunID+"/cancel", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cancel status %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	resp, err = http.Get(httpServer.URL + "/v1/sessions/" + sess.ID + "/events?after_seq=0")
	if err != nil {
		t.Fatal(err)
	}
	events := readSSE(t, resp)
	if len(events) == 0 || events[len(events)-1].Type != protocol.EventRunCancelled {
		t.Fatalf("cancel events = %+v", events)
	}
}

var _ = errors.Is
var _ = time.Second

func TestSubagentViewsUseAvailableTaskMetadata(t *testing.T) {
	start := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Minute)
	stored := session.Task{ID: "task-1", Description: "inspect", Status: "error", Report: "failed to inspect", StartedAt: start, EndedAt: end}
	view := storedSubagentView(stored)
	if view.Error != stored.Report || view.Report != stored.Report || view.StartedAt == nil || !view.StartedAt.Equal(start) || view.EndedAt == nil || !view.EndedAt.Equal(end) {
		t.Fatalf("stored task lost metadata: %+v", view)
	}
	if view.Attempt != 0 || view.UpdatedAt != nil || view.Model != (protocol.ModelRef{}) {
		t.Fatalf("invented unavailable metadata: %+v", view)
	}
	live := subagentView(agent.BackgroundTask{ID: "task-1", Status: agent.TaskRunning, SubModel: "model @ provider", StartedAt: start})
	if live.Model != (protocol.ModelRef{Model: "model", Provider: "provider"}) || live.EndedAt != nil || live.Error != "" {
		t.Fatalf("live task = %+v", live)
	}
}
