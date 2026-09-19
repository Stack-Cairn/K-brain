package acp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func TestSessionListWirePaginationAndProjectFilter(t *testing.T) {
	st := testStore(t)
	olderProject, newerProject := t.TempDir(), t.TempDir()
	all := make(map[string]bool)
	older := make(map[string]bool)
	for i := range sessionPageSize + 8 {
		cwd := newerProject
		if i < 3 {
			cwd = olderProject
		}
		id, err := st.Create(cwd, "m", "p")
		if err != nil {
			t.Fatal(err)
		}
		if err := st.Save(id, 0, []ai.Message{{Role: "user", Content: fmt.Sprint("session ", i)}}, "m", "p"); err != nil {
			t.Fatal(err)
		}
		all[id] = true
		if i < 3 {
			older[id] = true
		}
	}
	f := newFixture(t, nil, st, nil)
	f.initialize(t)
	first, err := f.conn.ListSessions(context.Background(), acp.ListSessionsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Sessions) != sessionPageSize || first.NextCursor == nil {
		t.Fatalf("first page count=%d cursor=%v", len(first.Sessions), first.NextCursor)
	}
	second, err := f.conn.ListSessions(context.Background(), acp.ListSessionsRequest{Cursor: first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Sessions) != 8 || second.NextCursor != nil {
		t.Fatalf("last page count=%d cursor=%v", len(second.Sessions), second.NextCursor)
	}
	for _, meta := range append(first.Sessions, second.Sessions...) {
		if !all[string(meta.SessionId)] {
			t.Fatalf("duplicate or unknown session %s", meta.SessionId)
		}
		delete(all, string(meta.SessionId))
	}
	if len(all) != 0 {
		t.Fatalf("missing sessions: %v", all)
	}
	filtered, err := f.conn.ListSessions(context.Background(), acp.ListSessionsRequest{Cwd: &olderProject})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered.Sessions) != 3 || filtered.NextCursor != nil {
		t.Fatalf("older project was truncated: %+v", filtered)
	}
	for _, meta := range filtered.Sessions {
		if !older[string(meta.SessionId)] || meta.Cwd != olderProject {
			t.Fatalf("wrong project session: %+v", meta)
		}
	}
	if _, err := f.conn.ListSessions(context.Background(), acp.ListSessionsRequest{Cwd: &olderProject, Cursor: first.NextCursor}); err == nil || !strings.Contains(err.Error(), "cursor") {
		t.Fatalf("reused cursor under different filter: %v", err)
	}
	empty, err := f.conn.ListSessions(context.Background(), acp.ListSessionsRequest{Cwd: new(t.TempDir())})
	if err != nil || empty.Sessions == nil || len(empty.Sessions) != 0 || empty.NextCursor != nil {
		t.Fatalf("empty result = %+v, %v", empty, err)
	}
}

func TestSessionCursorValidation(t *testing.T) {
	valid := sessionListCursor{Version: 1, After: session.PageCursor{UpdatedAt: time.Now().UTC(), ID: "abc"}, CWD: new("project")}
	encode := func(c sessionListCursor) string {
		data, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(data)
	}
	if got, err := decodeSessionCursor(encode(valid), valid.CWD); err != nil || got.After.ID != "abc" {
		t.Fatalf("valid cursor = %+v, %v", got, err)
	}
	invalid := []string{"", "!", strings.Repeat("x", 8193), base64.RawURLEncoding.EncodeToString([]byte("null")), encode(sessionListCursor{Version: 2, After: valid.After}), encode(sessionListCursor{Version: 1}), encode(sessionListCursor{Version: 1, After: session.PageCursor{UpdatedAt: time.Now(), ID: "../x"}})}
	data, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	invalid = append(invalid, base64.RawURLEncoding.EncodeToString(append(data, []byte(" {}")...)))
	invalid = append(invalid, base64.RawURLEncoding.EncodeToString([]byte(strings.Replace(string(data), `"version":1`, `"version":1,"unknown":true`, 1))))
	for _, raw := range invalid {
		if _, err := decodeSessionCursor(raw, valid.CWD); err == nil {
			t.Fatalf("accepted invalid cursor: %q", raw)
		}
	}
	for _, cwd := range []*string{nil, new("other"), new("")} {
		if _, err := decodeSessionCursor(encode(valid), cwd); err == nil {
			t.Fatalf("accepted changed filter: %v", cwd)
		}
	}
}
