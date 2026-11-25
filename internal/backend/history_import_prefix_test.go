package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

const historyImportURL = "/v1/migrations/liveagent-history"

// prefixRequest is version n of a legacy conversation that gains one message
// per version, as a source file still being appended to during migration.
func prefixRequest(n int) legacyHistoryImportRequest {
	created := time.Unix(100, 0).UTC()
	updated := time.Unix(int64(200+n), 0).UTC()
	all := []protocol.Message{
		{ID: "u1", Role: protocol.RoleUser, CreatedAt: &created, Content: []protocol.ContentBlock{{Type: protocol.ContentText, Text: "hello"}}},
		{ID: "a1", Role: protocol.RoleAssistant, Provider: "fixture", Model: "fixture-model", ToolCalls: []protocol.ToolCall{{ID: "call-1", Name: "lookup", Arguments: json.RawMessage(`{"q":"x"}`)}}, Content: []protocol.ContentBlock{{Type: protocol.ContentText, Text: "looking"}}},
		{ID: "t1", Role: protocol.RoleTool, ToolCallID: "call-1", Name: "lookup", Content: []protocol.ContentBlock{{Type: protocol.ContentText, Text: "result"}}},
		{ID: "a2", Role: protocol.RoleAssistant, Content: []protocol.ContentBlock{{Type: protocol.ContentText, Text: "answer"}}},
		{ID: "u2", Role: protocol.RoleUser, Content: []protocol.ContentBlock{{Type: protocol.ContentText, Text: "thanks"}}},
	}
	return legacyHistoryImportRequest{
		SourceID: "pi-prefix", ConversationID: "pi-prefix", SourceFingerprint: fmt.Sprintf("source-v%d", n),
		Title: "Source title", Model: protocol.ModelRef{Provider: "fixture", Model: "fixture-model"},
		CreatedAt: created, UpdatedAt: updated, Pinned: true, Shared: true, ShareToken: "share-token",
		SourceMetadata: json.RawMessage(fmt.Sprintf(`{"messages":%d}`, n)),
		Messages:       all[:n],
	}
}

func newPrefixServer(t *testing.T) (*session.Store, *httptest.Server, string) {
	t.Helper()
	root := t.TempDir()
	store, err := session.Open(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	_, server := newTestServer(t, store, filepath.Join(root, "events"), &scriptedClient{response: "unused"})
	return store, server, root
}

func postHistoryImport(t *testing.T, server *httptest.Server, in legacyHistoryImportRequest) (int, map[string]any) {
	t.Helper()
	out := map[string]any{}
	response := postJSON(t, http.DefaultClient, server.URL+historyImportURL, in, &out)
	return response.StatusCode, out
}

func wantImportConflict(t *testing.T, status int, body map[string]any, reason string) {
	t.Helper()
	if status != http.StatusConflict || body["errorCode"] != "import_conflict" || body["reason"] != reason {
		t.Fatalf("got %d %#v, want 409 import_conflict/%s", status, body, reason)
	}
	if body["error"] != `import source "pi-prefix" conflicts with existing session` {
		t.Fatalf("error text changed: %#v", body["error"])
	}
}

func TestHistoryImportPrefixUpdateHTTP(t *testing.T) {
	store, server, root := newPrefixServer(t)
	if status, body := postHistoryImport(t, server, prefixRequest(3)); status != http.StatusOK || body["status"] != "imported" || body["updated"] != nil {
		t.Fatalf("first import = %d %#v", status, body)
	}
	if err := store.SetTitle("pi-prefix", "Renamed"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetPinned("pi-prefix", false); err != nil {
		t.Fatal(err)
	}
	status, body := postHistoryImport(t, server, prefixRequest(4))
	if status != http.StatusOK || body["status"] != "imported" || body["updated"] != true || body["source_id"] != "pi-prefix" || body["backend_id"] != "pi-prefix" || body["checkpoint"] != "unresolved" {
		t.Fatalf("prefix update = %d %#v", status, body)
	}
	if body["fingerprint"] != importFingerprint(prefixRequest(4)) {
		t.Fatalf("fingerprint = %#v", body["fingerprint"])
	}
	// The LiveAgent client of the time accepts only these statuses.
	if s := body["status"]; s != "imported" && s != "already_imported" {
		t.Fatalf("status %v breaks old clients", s)
	}
	check := func(store *session.Store, n int) {
		t.Helper()
		meta, messages, err := store.Load("pi-prefix")
		if err != nil {
			t.Fatal(err)
		}
		if len(messages) != n || messages[n-1].ID != prefixRequest(n).Messages[n-1].ID {
			t.Fatalf("messages = %+v", messages)
		}
		if meta.Title != "Renamed" || meta.Pinned || !meta.Shared || meta.ShareToken != "share-token" {
			t.Fatalf("local state lost: %+v", meta)
		}
		if meta.ImportFingerprint != prefixRequest(n).SourceFingerprint || meta.ImportContentFingerprint != importFingerprint(prefixRequest(n)) || meta.ImportMetadata != string(prefixRequest(n).SourceMetadata) {
			t.Fatalf("import identity = %+v", meta)
		}
		if !meta.UpdatedAt.Equal(prefixRequest(n).UpdatedAt) {
			t.Fatalf("updated_at = %v", meta.UpdatedAt)
		}
	}
	check(store, 4)
	if status, body := postHistoryImport(t, server, prefixRequest(4)); status != http.StatusOK || body["status"] != "already_imported" || body["updated"] != nil {
		t.Fatalf("identical retry = %d %#v", status, body)
	}

	server.Close()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := session.Open(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	_, restarted := newTestServer(t, reopened, filepath.Join(root, "events"), &scriptedClient{response: "unused"})
	check(reopened, 4)
	if status, body := postHistoryImport(t, restarted, prefixRequest(4)); status != http.StatusOK || body["status"] != "already_imported" {
		t.Fatalf("identical after restart = %d %#v", status, body)
	}
	if status, body := postHistoryImport(t, restarted, prefixRequest(5)); status != http.StatusOK || body["updated"] != true {
		t.Fatalf("update after restart = %d %#v", status, body)
	}
	check(reopened, 5)
}

func TestHistoryImportPrefixConflictsHTTP(t *testing.T) {
	t.Run("diverged", func(t *testing.T) {
		_, server, _ := newPrefixServer(t)
		postHistoryImport(t, server, prefixRequest(3))
		changed := prefixRequest(4)
		changed.Messages = append([]protocol.Message(nil), changed.Messages...)
		changed.Messages[1].Content = []protocol.ContentBlock{{Type: protocol.ContentText, Text: "rewritten"}}
		status, body := postHistoryImport(t, server, changed)
		wantImportConflict(t, status, body, session.ImportConflictDiverged)
		status, body = postHistoryImport(t, server, prefixRequest(2))
		wantImportConflict(t, status, body, session.ImportConflictDiverged)
	})
	t.Run("no new messages", func(t *testing.T) {
		_, server, _ := newPrefixServer(t)
		postHistoryImport(t, server, prefixRequest(3))
		retitled := prefixRequest(3)
		retitled.SourceFingerprint, retitled.Title = "source-v3b", "New title"
		status, body := postHistoryImport(t, server, retitled)
		wantImportConflict(t, status, body, session.ImportConflictNoNewMessages)
	})
	t.Run("different source", func(t *testing.T) {
		store, server, _ := newPrefixServer(t)
		// A session made in K-brain under the id the import wants.
		id, err := store.Create("", "fixture-model", "fixture")
		if err != nil {
			t.Fatal(err)
		}
		in := prefixRequest(3)
		in.SourceID, in.ConversationID = id, id
		status, body := postHistoryImport(t, server, in)
		if status != http.StatusConflict || body["errorCode"] != "import_conflict" || body["reason"] != session.ImportConflictDifferentSource {
			t.Fatalf("different source = %d %#v", status, body)
		}
	})
	t.Run("modified after import", func(t *testing.T) {
		store, server, _ := newPrefixServer(t)
		postHistoryImport(t, server, prefixRequest(3))
		// A rewind that only dropped messages still leaves a prefix.
		if err := store.TruncateAtSequence("pi-prefix", 1); err != nil {
			t.Fatal(err)
		}
		status, body := postHistoryImport(t, server, prefixRequest(4))
		wantImportConflict(t, status, body, session.ImportConflictModifiedAfterImport)
		if len(store.RawMessages("pi-prefix")) != 1 {
			t.Fatal("conflict restored rewound messages")
		}
	})
	t.Run("local turn", func(t *testing.T) {
		store, server, _ := newPrefixServer(t)
		postHistoryImport(t, server, prefixRequest(3))
		raw := store.RawMessages("pi-prefix")
		if err := store.Save("pi-prefix", 0, append(raw, ai.Message{Role: "user", Content: "local"}), "fixture-model", "fixture"); err != nil {
			t.Fatal(err)
		}
		status, body := postHistoryImport(t, server, prefixRequest(5))
		wantImportConflict(t, status, body, session.ImportConflictModifiedAfterImport)
	})
	t.Run("deleted", func(t *testing.T) {
		_, server, _ := newPrefixServer(t)
		postHistoryImport(t, server, prefixRequest(3))
		if status := doJSON(t, http.MethodDelete, server.URL+"/v1/sessions/pi-prefix", nil, nil); status != http.StatusOK {
			t.Fatalf("delete = %d", status)
		}
		if status, body := postHistoryImport(t, server, prefixRequest(4)); status != http.StatusGone || body["errorCode"] != nil {
			t.Fatalf("deleted = %d %#v", status, body)
		}
	})
	t.Run("running turn", func(t *testing.T) {
		store, server, _ := newPrefixServer(t)
		postHistoryImport(t, server, prefixRequest(3))
		lease, err := store.AcquireRun("pi-prefix", "test")
		if err != nil {
			t.Fatal(err)
		}
		status, body := postHistoryImport(t, server, prefixRequest(4))
		if status != http.StatusConflict || body["errorCode"] != "session_busy" || body["reason"] != nil {
			t.Fatalf("busy = %d %#v", status, body)
		}
		_ = lease.Release()
		if status, body := postHistoryImport(t, server, prefixRequest(4)); status != http.StatusOK || body["updated"] != true {
			t.Fatalf("after run = %d %#v", status, body)
		}
	})
}

func TestHealthAdvertisesHistoryImportPrefixUpdate(t *testing.T) {
	caps := healthJSON(t, &Server{token: healthTestToken})["capabilities"].(map[string]any)
	if jsonInt(t, caps["history_import_prefix_update"], "history_import_prefix_update") != 1 {
		t.Fatalf("capabilities = %#v", caps)
	}
}

// An update imports checkpoints only for the turns it adds: snapshots of the
// turns already held (here consumed by a file rewind) are not resurrected.
func TestHistoryImportPrefixUpdateLeavesEarlierCheckpointsAlone(t *testing.T) {
	root := t.TempDir()
	store, err := session.Open(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, server := newTestServer(t, store, filepath.Join(root, "events"), &scriptedClient{response: "unused"})
	first := legacyCheckpointFixture(t)
	var result map[string]any
	if got := postJSON(t, http.DefaultClient, server.URL+historyImportURL, first, &result).StatusCode; got != http.StatusOK || result["checkpoint"] != "available" {
		t.Fatalf("first import = %d %#v", got, result)
	}
	if len(store.Snapshots("native")) != 2 {
		t.Fatalf("snapshots = %#v", store.Snapshots("native"))
	}
	if err := store.ClearSnapshots("native"); err != nil {
		t.Fatal(err)
	}
	next := legacyCheckpointFixture(t)
	next.SourceFingerprint = "fixture-v3"
	next.Messages = append(next.Messages, protocol.Message{ID: "u3", Role: protocol.RoleUser, Content: []protocol.ContentBlock{{Type: protocol.ContentText, Text: "third"}}})
	checkpoint := *next.Checkpoint
	checkpoint.Records = append(append([]legacyCheckpointImportRecord(nil), checkpoint.Records...), legacyCheckpointImportRecord{Schema: 2, TurnSeq: 9, TurnID: "u3", Kind: "turn", CapturedAt: 20})
	next.Checkpoint = &checkpoint
	result = map[string]any{}
	if got := postJSON(t, http.DefaultClient, server.URL+historyImportURL, next, &result).StatusCode; got != http.StatusOK || result["updated"] != true || result["checkpoint"] != "available" {
		t.Fatalf("update = %d %#v", got, result)
	}
	refs := store.Snapshots("native")
	if len(refs) != 1 || refs[3] == "" {
		t.Fatalf("snapshots after update = %#v", refs)
	}
}
