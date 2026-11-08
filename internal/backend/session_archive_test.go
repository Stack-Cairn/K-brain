package backend

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func archiveSession(t *testing.T, base, id string, archived bool) protocol.Session {
	t.Helper()
	var updated protocol.Session
	if code := doJSON(t, http.MethodPatch, base+"/v1/sessions/"+id, protocol.UpdateSessionRequest{Archived: &archived}, &updated); code != http.StatusOK {
		t.Fatalf("archive %s=%v status=%d", id, archived, code)
	}
	return updated
}

func listIDs(t *testing.T, base, query string) (ids []string, page protocol.SessionPage) {
	t.Helper()
	if code := doJSON(t, http.MethodGet, base+"/v1/sessions"+query, nil, &page); code != http.StatusOK {
		t.Fatalf("list %q status=%d", query, code)
	}
	for _, item := range page.Sessions {
		ids = append(ids, item.ID)
	}
	sort.Strings(ids)
	return ids, page
}

func sortedIDs(ids ...string) []string {
	out := append([]string(nil), ids...)
	sort.Strings(out)
	return out
}

func TestSessionListArchivedFilter(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, server := newTestServer(t, store, t.TempDir(), &scriptedClient{response: "ok"})
	cwd := t.TempDir()
	kept := createListedSession(t, server.URL, cwd, "kept")
	archived := createListedSession(t, server.URL, cwd, "archived")
	if view := archiveSession(t, server.URL, archived.ID, true); !view.Archived {
		t.Fatalf("PATCH response archived=%v", view.Archived)
	}

	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"", []string{kept.ID}},
		{"?archived=exclude", []string{kept.ID}},
		{"?archived=include", sortedIDs(kept.ID, archived.ID)},
		{"?archived=only", []string{archived.ID}},
		{"?archived=only&cwd=" + cwd, []string{archived.ID}},
	} {
		got, page := listIDs(t, server.URL, tc.query)
		if strings.Join(got, ",") != strings.Join(tc.want, ",") || page.TotalCount != len(tc.want) {
			t.Fatalf("list %q = %v (total %d), want %v", tc.query, got, page.TotalCount, tc.want)
		}
		for _, item := range page.Sessions {
			if item.Archived != (item.ID == archived.ID) {
				t.Fatalf("list %q: %s archived=%v", tc.query, item.ID, item.Archived)
			}
		}
	}

	// archived is always on the wire, false included.
	resp, err := http.Get(server.URL + "/v1/sessions?archived=include")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var wire struct {
		Sessions []map[string]json.RawMessage `json:"sessions"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	for _, item := range wire.Sessions {
		if _, ok := item["archived"]; !ok {
			t.Fatalf("summary without archived: %s", raw)
		}
	}

	for _, endpoint := range []string{"/v1/sessions", "/v1/sessions/workdirs", "/v1/sessions/changes?epoch=x&since=0"} {
		sep := "?"
		if strings.Contains(endpoint, "?") {
			sep = "&"
		}
		if code := doJSON(t, http.MethodGet, server.URL+endpoint+sep+"archived=all", nil, nil); code != http.StatusBadRequest {
			t.Fatalf("%s archived=all status=%d, want 400", endpoint, code)
		}
	}
	if code := doJSON(t, http.MethodPost, server.URL+"/v1/sessions/snapshots?archived=yes", nil, nil); code != http.StatusBadRequest {
		t.Fatalf("snapshot archived=yes status=%d, want 400", code)
	}

	// Unarchiving brings it back to the default list.
	archiveSession(t, server.URL, archived.ID, false)
	if got, _ := listIDs(t, server.URL, ""); strings.Join(got, ",") != strings.Join(sortedIDs(kept.ID, archived.ID), ",") {
		t.Fatalf("after unarchive list = %v", got)
	}
	if got, _ := listIDs(t, server.URL, "?archived=only"); len(got) != 0 {
		t.Fatalf("after unarchive only = %v", got)
	}
}

func TestSessionSnapshotArchivedFilterAndWorkdirs(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, server := newTestServer(t, store, t.TempDir(), &scriptedClient{response: "ok"})
	visibleCWD, archivedCWD := t.TempDir(), t.TempDir()
	createListedSession(t, server.URL, visibleCWD, "visible")
	hidden := createListedSession(t, server.URL, archivedCWD, "hidden")
	archiveSession(t, server.URL, hidden.ID, true)

	for _, tc := range []struct {
		query    string
		total    int
		workdirs int
	}{{"", 1, 1}, {"?archived=include", 2, 2}, {"?archived=only", 1, 1}} {
		var info protocol.SessionSnapshotInfo
		if code := doJSON(t, http.MethodPost, server.URL+"/v1/sessions/snapshots"+tc.query, nil, &info); code != http.StatusOK {
			t.Fatalf("snapshot %q status=%d", tc.query, code)
		}
		if info.TotalCount != tc.total || len(info.Workdirs) != tc.workdirs {
			t.Fatalf("snapshot %q = %+v", tc.query, info)
		}
		var page protocol.SessionSnapshotPage
		if code := doJSON(t, http.MethodGet, server.URL+"/v1/sessions/snapshots/"+info.SnapshotID, nil, &page); code != http.StatusOK {
			t.Fatalf("snapshot page status=%d", code)
		}
		if tc.query == "?archived=only" && (len(page.Sessions) != 1 || page.Sessions[0].ID != hidden.ID || !page.Sessions[0].Archived) {
			t.Fatalf("archived-only snapshot = %+v", page.Sessions)
		}
		_ = doJSON(t, http.MethodDelete, server.URL+"/v1/sessions/snapshots/"+info.SnapshotID, nil, nil)

		var workdirs struct {
			Workdirs []protocol.SessionWorkdir `json:"workdirs"`
		}
		if code := doJSON(t, http.MethodGet, server.URL+"/v1/sessions/workdirs"+tc.query, nil, &workdirs); code != http.StatusOK || len(workdirs.Workdirs) != tc.workdirs {
			t.Fatalf("workdirs %q = %d %+v", tc.query, code, workdirs)
		}
	}
}

func changesSince(t *testing.T, base, epoch, since, extra string) protocol.SessionChanges {
	t.Helper()
	var out protocol.SessionChanges
	if code := doJSON(t, http.MethodGet, base+"/v1/sessions/changes?epoch="+epoch+"&since="+since+"&wait=0"+extra, nil, &out); code != http.StatusOK {
		t.Fatalf("changes status=%d", code)
	}
	if out.Reset {
		t.Fatalf("changes reset: %+v", out)
	}
	return out
}

func onlyChange(t *testing.T, changes protocol.SessionChanges, id string) protocol.SessionChange {
	t.Helper()
	if len(changes.Changes) != 1 || changes.Changes[0].ID != id || changes.Changes[0].Kind != "upsert" || changes.Changes[0].Session == nil {
		t.Fatalf("want one upsert for %s, got %+v", id, changes.Changes)
	}
	return changes.Changes[0]
}

// An archive or unarchive is an upsert: on the default filter listed flips, on
// archived=include the session stays listed with archived flipped, and on
// archived=only listed follows the flag.
func TestSessionChangesReportArchiveToggles(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, server := newTestServer(t, store, t.TempDir(), &scriptedClient{response: "ok"})
	item := createListedSession(t, server.URL, t.TempDir(), "toggle")
	var info protocol.SessionSnapshotInfo
	if code := doJSON(t, http.MethodPost, server.URL+"/v1/sessions/snapshots?archived=include", nil, &info); code != http.StatusOK {
		t.Fatal(code)
	}

	archivedView := archiveSession(t, server.URL, item.ID, true)
	type want struct{ listed, archived bool }
	check := func(since string, cases map[string]want) string {
		t.Helper()
		next := ""
		for extra, w := range cases {
			change := onlyChange(t, changesSince(t, server.URL, info.Epoch, since, extra), item.ID)
			if change.Listed != w.listed || change.Session.Archived != w.archived {
				t.Fatalf("changes%s: listed=%v archived=%v, want %+v", extra, change.Listed, change.Session.Archived, w)
			}
			next = change.Revision
		}
		return next
	}
	afterArchive := check(info.Revision, map[string]want{
		"":                  {listed: false, archived: true},
		"&archived=exclude": {listed: false, archived: true},
		"&archived=include": {listed: true, archived: true},
		"&archived=only":    {listed: true, archived: true},
	})
	if afterArchive != archivedView.SyncRevision {
		t.Fatalf("change revision %s, PATCH sync_revision %s", afterArchive, archivedView.SyncRevision)
	}

	archiveSession(t, server.URL, item.ID, false)
	check(afterArchive, map[string]want{
		"":                  {listed: true, archived: false},
		"&archived=include": {listed: true, archived: false},
		"&archived=only":    {listed: false, archived: false},
	})
}

// The terminal UI archives through its own store on the same files; the
// backend's changes feed must still report it.
func TestSessionChangesReportExternalArchive(t *testing.T) {
	dir := t.TempDir()
	store, err := session.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, server := newTestServer(t, store, t.TempDir(), &scriptedClient{response: "ok"})
	item := createListedSession(t, server.URL, t.TempDir(), "external")
	var info protocol.SessionSnapshotInfo
	if code := doJSON(t, http.MethodPost, server.URL+"/v1/sessions/snapshots", nil, &info); code != http.StatusOK {
		t.Fatal(code)
	}

	other, err := session.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := other.SetArchived(item.ID, true); err != nil {
		t.Fatal(err)
	}

	change := onlyChange(t, changesSince(t, server.URL, info.Epoch, info.Revision, "&rescan=1"), item.ID)
	if change.Listed || !change.Session.Archived {
		t.Fatalf("external archive change = listed %v archived %v", change.Listed, change.Session.Archived)
	}
	change = onlyChange(t, changesSince(t, server.URL, info.Epoch, info.Revision, "&archived=include"), item.ID)
	if !change.Listed || !change.Session.Archived {
		t.Fatalf("external archive change (include) = listed %v archived %v", change.Listed, change.Session.Archived)
	}
	if got, _ := listIDs(t, server.URL, "?archived=only"); len(got) != 1 || got[0] != item.ID {
		t.Fatalf("archived-only list after external archive = %v", got)
	}
}
