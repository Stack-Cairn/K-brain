package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

// newProviderTestServer runs the backend on a real wire client, so a test can
// drive it from a fake provider endpoint.
func newProviderTestServer(t *testing.T, store *session.Store, client ai.Client) *httptest.Server {
	t.Helper()
	backendServer, err := New(Options{
		Store: store,
		Factory: func(_ context.Context, _ string, selected protocol.ModelRef) (*agent.Agent, error) {
			ag := agent.New(client, selected.Model, 1024, "system")
			ag.ModelName = selected.Model
			ag.Provider = selected.Provider
			return ag, nil
		},
		EventDir:   t.TempDir(),
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
	return httpServer
}

// sseFixture answers every model request with the given SSE data events.
func sseFixture(t *testing.T, events ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range events {
			fmt.Fprintf(w, "data: %s\n\n", event)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Reasoning streamed by a Responses provider stays in history after the run,
// as a "thinking" content block ahead of the reply text.
func TestServerHistoryKeepsResponsesReasoning(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	provider := sseFixture(t,
		`{"type":"response.reasoning_summary_text.delta","item_id":"rs","summary_index":0,"delta":"Adding the "}`,
		`{"type":"response.reasoning_summary_text.delta","item_id":"rs","summary_index":0,"delta":"numbers."}`,
		`{"type":"response.output_item.done","item":{"type":"reasoning","id":"rs","summary":[{"type":"summary_text","text":"Adding the numbers."}],"encrypted_content":"sealed"}}`,
		`{"type":"response.output_text.delta","delta":"4"}`,
		`{"type":"response.completed","response":{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"4"}]}]}}`,
	)
	server := newProviderTestServer(t, store, ai.NewResponses(provider.URL, "key"))
	sess := createTestSession(t, server.URL)
	runRequest(t, server.URL, sess.ID, "reasoning", "2+2?")
	events := waitForRun(t, server.URL, sess.ID, 0)
	if events[len(events)-1].Type != protocol.EventRunCompleted {
		t.Fatalf("run events = %+v", events)
	}

	var page historyResponse
	if code := doJSON(t, http.MethodGet, server.URL+"/v1/sessions/"+sess.ID+"/history?max_messages=100", nil, &page); code != http.StatusOK {
		t.Fatalf("history status %d", code)
	}
	messages := page.Session.Messages
	if len(messages) != 2 || messages[1].Role != protocol.RoleAssistant {
		t.Fatalf("history = %+v", messages)
	}
	content := messages[1].Content
	if len(content) != 2 || content[0].Type != protocol.ContentThinking || content[0].Text != "Adding the numbers." || content[1].Type != protocol.ContentText || content[1].Text != "4" {
		t.Fatalf("assistant content = %+v", content)
	}
}

// The run's closing assistant.message.created keeps its earlier shape: clients
// merge it into the thinking they streamed, so it carries no thinking block.
func TestServerAssistantEventLeavesThinkingToHistory(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	provider := sseFixture(t,
		`{"type":"response.reasoning_summary_text.delta","item_id":"rs","summary_index":0,"delta":"Adding."}`,
		`{"type":"response.output_text.delta","delta":"4"}`,
		`{"type":"response.completed","response":{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"4"}]}]}}`,
	)
	server := newProviderTestServer(t, store, ai.NewResponses(provider.URL, "key"))
	sess := createTestSession(t, server.URL)
	runRequest(t, server.URL, sess.ID, "reasoning-event", "2+2?")
	var created []protocol.Message
	for _, event := range waitForRun(t, server.URL, sess.ID, 0) {
		if event.Type == protocol.EventAssistantMessage {
			var message protocol.Message
			if err := json.Unmarshal(event.Payload, &message); err != nil {
				t.Fatal(err)
			}
			created = append(created, message)
		}
	}
	if len(created) != 1 || len(created[0].Content) != 1 || created[0].Content[0].Type != protocol.ContentText || created[0].Content[0].Text != "4" {
		t.Fatalf("assistant.message.created = %+v", created)
	}
}
