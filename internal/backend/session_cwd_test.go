package backend

import (
	"net/http"
	"path/filepath"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func TestSessionCWDPatchMovesLoadedSessionToAnotherWorkspace(t *testing.T) {
	dir, events := t.TempDir(), t.TempDir()
	store, err := session.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &scriptedClient{response: "unused"}
	backendServer, server := newTestServer(t, store, events, fixture)
	from, to := t.TempDir(), t.TempDir()
	id := createStoredHistorySession(t, store, from, []ai.Message{{Role: protocol.RoleUser, Content: "move me"}})
	rt, err := backendServer.loadRuntimeByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if rt.agent == nil || rt.agent.WorkingDir != from {
		t.Fatalf("loaded agent working dir=%v, want %q", rt.agent, from)
	}

	body := protocol.UpdateSessionRequest{CWD: &to}
	var updated protocol.Session
	if status := doJSON(t, http.MethodPatch, server.URL+"/v1/sessions/"+id, body, &updated); status != http.StatusOK {
		t.Fatalf("PATCH status=%d", status)
	}
	if updated.CWD != filepath.Clean(to) {
		t.Fatalf("patched cwd=%q, want %q", updated.CWD, to)
	}
	rt.mu.Lock()
	workingDir, model := rt.agent.WorkingDir, rt.agent.ModelName
	rt.mu.Unlock()
	if workingDir != filepath.Clean(to) || model != "fixture-model" {
		t.Fatalf("rebuilt agent dir=%q model=%q", workingDir, model)
	}
	if got := getSession(t, server.URL, id); got.CWD != filepath.Clean(to) {
		t.Fatalf("stored cwd=%q", got.CWD)
	}
	if page := getSessionPage(t, server.URL+"/v1/sessions?cwd="+to); page.TotalCount != 1 {
		t.Fatalf("sessions in new workspace=%d", page.TotalCount)
	}
	if page := getSessionPage(t, server.URL+"/v1/sessions?cwd="+from); page.TotalCount != 0 {
		t.Fatalf("sessions left in old workspace=%d", page.TotalCount)
	}
}

func TestSessionCWDPatchRejectsRelativeOrMissingDirectories(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, server := newTestServer(t, store, t.TempDir(), &scriptedClient{response: "unused"})
	from := t.TempDir()
	id := createStoredHistorySession(t, store, from, []ai.Message{{Role: protocol.RoleUser, Content: "stay"}})
	for _, cwd := range []string{"", "relative/dir", filepath.Join(t.TempDir(), "missing")} {
		body := protocol.UpdateSessionRequest{CWD: &cwd}
		if status := doJSON(t, http.MethodPatch, server.URL+"/v1/sessions/"+id, body, nil); status != http.StatusBadRequest {
			t.Fatalf("cwd %q status=%d, want 400", cwd, status)
		}
	}
	if got := getSession(t, server.URL, id); got.CWD != from {
		t.Fatalf("cwd changed to %q after rejected patches", got.CWD)
	}
}
