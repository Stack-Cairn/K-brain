package backend

import (
	"encoding/json"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

// A backend shutdown during a run is saved apart from a user stop: the run
// still ends with run.cancelled{state:"cancelled"} and stop_reason
// "cancelled", but both carry cancel_reason "shutdown", whether the model had
// streamed part of a reply or nothing yet.
func TestServerShutdownMarksRunInterrupted(t *testing.T) {
	for _, tc := range []struct {
		name    string
		partial string
	}{
		{"after output", "Half of the answer"},
		{"before output", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := session.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			eventDir := t.TempDir()
			fixture := &scriptedClient{wait: true, partial: tc.partial, streamed: make(chan struct{}, 1)}
			backendServer, httpServer := newTestServer(t, store, eventDir, fixture)
			sess := createTestSession(t, httpServer.URL)
			accepted := runRequest(t, httpServer.URL, sess.ID, "shutdown", "explain")
			waitStreamed(t, fixture.streamed)
			if err := backendServer.Close(); err != nil {
				t.Fatal(err)
			}
			httpServer.Close()
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}

			reopened, err := session.Open(store.SessionsDir())
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			_, restarted := newTestServer(t, reopened, eventDir, &scriptedClient{response: "ok"})
			messages := getSession(t, restarted.URL, sess.ID).Messages
			if len(messages) != 2 || messages[0].Role != protocol.RoleUser {
				t.Fatalf("history after shutdown = %+v", messages)
			}
			reply := messages[1]
			if reply.Role != protocol.RoleAssistant || reply.StopReason != "cancelled" || reply.CancelReason != "shutdown" {
				t.Fatalf("interrupted reply = %+v", reply)
			}
			if tc.partial == "" {
				if reply.ID != "assistant-stopped-"+accepted.RunID || len(reply.Content) != 0 {
					t.Fatalf("interrupted marker = %+v", reply)
				}
			} else if len(reply.Content) != 1 || reply.Content[0].Text != tc.partial {
				t.Fatalf("interrupted reply content = %+v", reply.Content)
			}

			restored := &runtimeSession{id: sess.ID, runs: map[string]runRecord{}}
			if err := restored.loadJournal(eventDir); err != nil {
				t.Fatal(err)
			}
			var terminal *protocol.RunTerminal
			for _, event := range restored.events {
				if event.Type == protocol.EventRunCancelled {
					terminal = &protocol.RunTerminal{}
					if err := json.Unmarshal(event.Payload, terminal); err != nil {
						t.Fatal(err)
					}
				}
			}
			if terminal == nil || terminal.State != "cancelled" || terminal.CancelReason != "shutdown" {
				t.Fatalf("run.cancelled after shutdown = %+v", terminal)
			}
		})
	}
}
