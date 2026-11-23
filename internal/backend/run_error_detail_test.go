package backend

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

// runFailure runs one prompt and returns the run.failed terminal and the
// assistant.message.created events it published.
func runFailure(t *testing.T, base, sessionID, requestID string) (protocol.RunTerminal, []protocol.Message) {
	t.Helper()
	before := getSession(t, base, sessionID).LastSeq
	runRequest(t, base, sessionID, requestID, "hello")
	var terminal protocol.RunTerminal
	var created []protocol.Message
	for _, event := range waitForRun(t, base, sessionID, before) {
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
	if terminal.State != "failed" {
		t.Fatalf("terminal = %+v", terminal)
	}
	return terminal, created
}

// A provider that rejects the request gives a failed run whose terminal event
// and saved "assistant-error-<run>" message carry the failure kind, the HTTP
// status and the provider's message without credentials, next to the raw
// error text.
func TestServerRunFailureCarriesErrorDetail(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   protocol.ErrorDetail
	}{
		{"invalid key", 401, `{"error":{"message":"Incorrect API key provided: sk-live-abcdefgh12345678","type":"invalid_request_error"}}`, protocol.ErrorDetail{Kind: "invalid_api_key", UpstreamStatus: 401, ProviderMessage: "Incorrect API key provided: [redacted]"}},
		{"rate limited", 429, `{"error":{"message":"Rate limit reached"}}`, protocol.ErrorDetail{Kind: "rate_limited", UpstreamStatus: 429, ProviderMessage: "Rate limit reached"}},
		{"server error", 500, `<html>oops</html>`, protocol.ErrorDetail{Kind: "server_error", UpstreamStatus: 500}},
		{"model missing", 404, `{"error":{"message":"The model does not exist"}}`, protocol.ErrorDetail{Kind: "not_found", UpstreamStatus: 404, ProviderMessage: "The model does not exist"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer upstream.Close()
			client := ai.New(upstream.URL, "key")
			client.MaxRetries = 1
			store, err := session.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			backendServer, httpServer := startProviderBackend(t, store, t.TempDir(), client)
			defer func() {
				httpServer.Close()
				_ = backendServer.Close()
			}()
			sess := createTestSession(t, httpServer.URL)
			terminal, created := runFailure(t, httpServer.URL, sess.ID, "rejected")
			if terminal.Error == "" || terminal.ErrorDetail == nil || *terminal.ErrorDetail != tc.want {
				t.Fatalf("terminal = %+v detail=%+v", terminal, terminal.ErrorDetail)
			}
			if len(created) != 1 || created[0].ErrorDetail == nil || *created[0].ErrorDetail != tc.want || created[0].StopReason != string(ai.StopReasonError) {
				t.Fatalf("assistant.message.created = %+v", created)
			}
			history := getSession(t, httpServer.URL, sess.ID).Messages
			last := history[len(history)-1]
			if last.ID != created[0].ID || last.ErrorDetail == nil || *last.ErrorDetail != tc.want || len(last.Content) != 1 || last.Content[0].Text != terminal.Error {
				t.Fatalf("saved failure = %+v", last)
			}
		})
	}
}

// The partial reply kept when a stream drops carries the same structured
// failure as the run.failed event.
func TestServerPartialReplyCarriesErrorDetail(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	upstream := httptest.NewServer(&droppingProvider{})
	defer upstream.Close()
	backendServer, httpServer := startProviderBackend(t, store, t.TempDir(), ai.New(upstream.URL, "key"))
	defer func() {
		httpServer.Close()
		_ = backendServer.Close()
	}()
	sess := createTestSession(t, httpServer.URL)
	terminal, created := runFailure(t, httpServer.URL, sess.ID, "dropped")
	want := protocol.ErrorDetail{Kind: "connection_lost"}
	if terminal.ErrorDetail == nil || *terminal.ErrorDetail != want {
		t.Fatalf("terminal = %+v detail=%+v", terminal, terminal.ErrorDetail)
	}
	if len(created) != 1 || created[0].Error != terminal.Error || created[0].ErrorDetail == nil || *created[0].ErrorDetail != want {
		t.Fatalf("assistant.message.created = %+v", created)
	}
	history := getSession(t, httpServer.URL, sess.ID).Messages
	if last := history[len(history)-1]; last.ID != created[0].ID || last.ErrorDetail == nil || *last.ErrorDetail != want || last.Content[len(last.Content)-1].Text != "The capital is Par" {
		t.Fatalf("saved partial reply = %+v", last)
	}
}
