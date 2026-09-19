package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	acp "github.com/coder/acp-go-sdk"
)

func TestPromptCompactionPersistsAcrossResume(t *testing.T) {
	for _, saveSystem := range []bool{true, false} {
		t.Run(fmt.Sprintf("stored-system=%v", saveSystem), func(t *testing.T) {
			testPromptCompactionPersistsAcrossResume(t, saveSystem)
		})
	}
}

func testPromptCompactionPersistsAcrossResume(t *testing.T, saveSystem bool) {
	st := testStore(t)
	dir := t.TempDir()
	id, err := st.Create(dir, "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	history := []ai.Message{{Role: "system", Content: "sys"}}
	for i := range 8 {
		history = append(history, ai.Message{Role: "user", Content: fmt.Sprintf("question %d %s", i, strings.Repeat("long history ", 200))}, ai.Message{Role: "assistant", Content: fmt.Sprintf("answer %d", i)})
	}
	from := 0
	if !saveSystem {
		from = 1
	}
	if err := st.Save(id, from, history, "m", "p"); err != nil {
		t.Fatal(err)
	}
	var compactions atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ai.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if !req.Stream {
			compactions.Add(1)
			fmt.Fprint(w, `{"choices":[{"message":{"content":"folded summary"}}],"usage":{"prompt_tokens":100,"completion_tokens":10}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"continued\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	f := newFixture(t, nil, st, factoryFor(srv, nil))
	f.initialize(t)
	sid := acp.SessionId(id)
	if err := restoreForTest(context.Background(), f.bridge, sid, dir, false); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		s := f.bridge.getSession(sid)
		s.ag.ContextLimit = 400
		s.ag.CompactThreshold = 0.1
		if _, err := f.prompt(t, sid, fmt.Sprintf("continue %d", i)); err != nil {
			t.Fatal(err)
		}
		want := s.ag.MessagesSnapshot()
		_, got, err := st.Load(id)
		if err != nil {
			t.Fatal(err)
		}
		if !sameMessages(t, got, want[from:]) {
			t.Fatalf("persisted context differs after compaction %d", i)
		}
		if _, err := f.conn.CloseSession(context.Background(), acp.CloseSessionRequest{SessionId: sid}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.conn.ResumeSession(context.Background(), acp.ResumeSessionRequest{SessionId: sid, Cwd: dir, McpServers: []acp.McpServer{}}); err != nil {
			t.Fatal(err)
		}
		if !sameMessages(t, f.bridge.getSession(sid).ag.MessagesSnapshot(), want) {
			t.Fatalf("resumed context differs after compaction %d", i)
		}
	}
	if compactions.Load() != 2 || len(st.Compactions(id)) != 2 {
		t.Fatalf("compactions: model=%d stored=%d", compactions.Load(), len(st.Compactions(id)))
	}
	raw := st.RawMessages(id)
	history = history[from:]
	if len(raw) != len(history)+4 || !sameMessages(t, raw[:len(history)], history) {
		t.Fatal("raw history overwritten")
	}
}

func sameMessages(t *testing.T, a, b []ai.Message) bool {
	t.Helper()
	left, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	right, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return reflect.DeepEqual(left, right)
}
