package backend

import (
	"net/http"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

// A running turn must not block store-only metadata updates: pinned and
// archived have no turn-side writer and the store update serializes with the
// turn under rt.mu. Runtime-touching fields keep the idle requirement.
func TestUpdateSessionMetadataDuringActiveTurn(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, server := newTestServer(t, store, t.TempDir(), &scriptedClient{wait: true})
	sess := createTestSession(t, server.URL)
	run := postJSON(t, http.DefaultClient, server.URL+"/v1/sessions/"+sess.ID+"/runs", protocol.PromptRequest{ClientRequestID: "active-turn", Prompt: "hello"}, nil)
	run.Body.Close()

	deadline := time.Now().Add(5 * time.Second)
	for {
		rt, err := service.loadRuntimeByID(sess.ID)
		if err != nil {
			t.Fatal(err)
		}
		rt.mu.Lock()
		active := rt.activeLocked()
		rt.mu.Unlock()
		service.releaseRuntime(rt)
		if active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("turn never became active")
		}
		time.Sleep(10 * time.Millisecond)
	}

	pinned, archived := true, true
	var updated protocol.Session
	if code := doJSON(t, http.MethodPatch, server.URL+"/v1/sessions/"+sess.ID, protocol.UpdateSessionRequest{Pinned: &pinned}, &updated); code != http.StatusOK {
		t.Fatalf("pinned during turn: status=%d", code)
	}
	if code := doJSON(t, http.MethodPatch, server.URL+"/v1/sessions/"+sess.ID, protocol.UpdateSessionRequest{Archived: &archived}, &updated); code != http.StatusOK {
		t.Fatalf("archived during turn: status=%d", code)
	}

	title := "renamed mid-turn"
	if code := doJSON(t, http.MethodPatch, server.URL+"/v1/sessions/"+sess.ID, protocol.UpdateSessionRequest{Title: &title}, &updated); code != http.StatusConflict {
		t.Fatalf("title during turn: status=%d", code)
	}
	model := protocol.ModelRef{Provider: "fixture", Model: "fixture-model"}
	if code := doJSON(t, http.MethodPatch, server.URL+"/v1/sessions/"+sess.ID, protocol.UpdateSessionRequest{Model: &model}, &updated); code != http.StatusConflict {
		t.Fatalf("model during turn: status=%d", code)
	}
	cwd := t.TempDir()
	if code := doJSON(t, http.MethodPatch, server.URL+"/v1/sessions/"+sess.ID, protocol.UpdateSessionRequest{CWD: &cwd}, &updated); code != http.StatusConflict {
		t.Fatalf("cwd during turn: status=%d", code)
	}
}
