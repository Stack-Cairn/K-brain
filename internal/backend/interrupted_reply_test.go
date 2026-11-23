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

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

// droppingProvider is a Chat Completions endpoint whose first response drops
// mid-reply; later responses finish normally. It records every request body.
type droppingProvider struct {
	mu     sync.Mutex
	bodies []string
}

func (p *droppingProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	p.mu.Lock()
	p.bodies = append(p.bodies, string(body))
	first := len(p.bodies) == 1
	p.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	if first {
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"Recalling the facts.\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"The capital is Par\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call-cut\",\"function\":{\"name\":\"read\",\"arguments\":\"{\\\"pa\"}}]}}]}\n\n")
		return
	}
	fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Paris.\"},\"finish_reason\":\"stop\"}]}\n\n")
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func (p *droppingProvider) body(t *testing.T, i int) string {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if i >= len(p.bodies) {
		t.Fatalf("request %d was not sent (%d requests)", i, len(p.bodies))
	}
	return p.bodies[i]
}

func startProviderBackend(t *testing.T, store *session.Store, eventDir string, client ai.Client) (*Server, *httptest.Server) {
	t.Helper()
	backendServer, err := New(Options{
		Store: store,
		Factory: func(_ context.Context, _ string, selected protocol.ModelRef) (*agent.Agent, error) {
			ag := agent.New(client, selected.Model, 1024, "system")
			ag.ModelName = selected.Model
			ag.Provider = selected.Provider
			return ag, nil
		},
		EventDir:   eventDir,
		Models:     []protocol.ModelRef{{Provider: "fixture", Model: "fixture-model"}},
		DefaultCWD: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return backendServer, httptest.NewServer(backendServer)
}

// A model stream that drops mid-reply keeps the streamed part in history as
// the run's failed assistant message (stop_reason "error" with the failure in
// "error"), survives a restart, and the next run sends a valid request with the
// text marked as interrupted and the cut-off tool call left out.
func TestServerKeepsPartialReplyWhenStreamDrops(t *testing.T) {
	sessionsDir, eventDir := t.TempDir(), t.TempDir()
	store, err := session.Open(sessionsDir)
	if err != nil {
		t.Fatal(err)
	}
	provider := &droppingProvider{}
	upstream := httptest.NewServer(provider)
	defer upstream.Close()
	client := ai.New(upstream.URL, "key")
	backendServer, httpServer := startProviderBackend(t, store, eventDir, client)
	sess := createTestSession(t, httpServer.URL)

	runRequest(t, httpServer.URL, sess.ID, "dropped", "Capital of France?")
	var created []protocol.Message
	var terminal protocol.RunTerminal
	for _, event := range waitForRun(t, httpServer.URL, sess.ID, 0) {
		switch event.Type {
		case protocol.EventAssistantMessage:
			var message protocol.Message
			if err := json.Unmarshal(event.Payload, &message); err != nil {
				t.Fatal(err)
			}
			created = append(created, message)
		case protocol.EventRunFailed:
			if err := json.Unmarshal(event.Payload, &terminal); err != nil {
				t.Fatal(err)
			}
		}
	}
	if terminal.State != "failed" || terminal.Error == "" {
		t.Fatalf("terminal = %+v", terminal)
	}
	if len(created) != 1 || created[0].StopReason != string(ai.StopReasonError) || created[0].Error != terminal.Error ||
		len(created[0].Content) != 1 || created[0].Content[0].Type != protocol.ContentText || created[0].Content[0].Text != "The capital is Par" || len(created[0].ToolCalls) != 0 {
		t.Fatalf("assistant.message.created = %+v", created)
	}

	checkHistory := func(base string) {
		t.Helper()
		history := getSession(t, base, sess.ID)
		if len(history.Messages) != 2 {
			t.Fatalf("history = %+v", history.Messages)
		}
		reply := history.Messages[1]
		if reply.Role != protocol.RoleAssistant || reply.ID != created[0].ID || reply.StopReason != string(ai.StopReasonError) || reply.Error != terminal.Error || len(reply.ToolCalls) != 0 {
			t.Fatalf("failed reply = %+v", reply)
		}
		if len(reply.Content) != 2 || reply.Content[0].Type != protocol.ContentThinking || reply.Content[0].Text != "Recalling the facts." || reply.Content[1].Text != "The capital is Par" {
			t.Fatalf("failed reply content = %+v", reply.Content)
		}
	}
	checkHistory(httpServer.URL)

	httpServer.Close()
	if err := backendServer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = session.Open(sessionsDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	backendServer, httpServer = startProviderBackend(t, store, eventDir, client)
	defer func() {
		httpServer.Close()
		_ = backendServer.Close()
	}()
	checkHistory(httpServer.URL)

	before := getSession(t, httpServer.URL, sess.ID).LastSeq
	runRequest(t, httpServer.URL, sess.ID, "after-drop", "Say it again")
	events := waitForRun(t, httpServer.URL, sess.ID, before)
	if events[len(events)-1].Type != protocol.EventRunCompleted {
		t.Fatalf("next run events = %+v", events)
	}
	var next struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal([]byte(provider.body(t, 1)), &next); err != nil {
		t.Fatal(err)
	}
	roles := make([]any, len(next.Messages))
	for i, m := range next.Messages {
		roles[i] = m["role"]
	}
	if fmt.Sprint(roles) != "[system user assistant user]" || next.Messages[2]["content"] != "The capital is Par\n\n[response interrupted by an error]" || next.Messages[2]["tool_calls"] != nil {
		t.Fatalf("next request messages = %+v", next.Messages)
	}
	if body := provider.body(t, 1); strings.Contains(body, terminal.Error) || strings.Contains(body, "Recalling the facts") {
		t.Fatalf("display-only fields reached the provider: %s", body)
	}
}
