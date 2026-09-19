package acp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	acp "github.com/coder/acp-go-sdk"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/mcp"
	"github.com/Stack-Cairn/K-brain/internal/session"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func testStore(t *testing.T) *session.Store {
	t.Helper()
	st, err := session.Open(t.TempDir() + "/sessions")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestPromptPersistsToStore(t *testing.T) {
	st := testStore(t)
	srv := scriptServer(t, []step{{text: "remember me"}})
	f := newFixture(t, nil, st, factoryFor(srv, nil))
	f.initialize(t)
	id := f.newSession(t, t.TempDir())

	if _, err := f.prompt(t, id, "hello store"); err != nil {
		t.Fatal(err)
	}

	_, msgs, err := st.Load(string(id))
	if err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	var sawUser, sawAssistant bool
	for _, m := range msgs {
		if m.Role == "user" && m.Content == "hello store" {
			sawUser = true
		}
		if m.Role == "assistant" && strings.Contains(m.Content, "remember me") {
			sawAssistant = true
		}
	}
	if !sawUser || !sawAssistant {
		t.Errorf("stored messages missing: user=%v assistant=%v (%d msgs)", sawUser, sawAssistant, len(msgs))
	}

	if _, err := f.prompt(t, id, "again"); err != nil {
		t.Fatal(err)
	}
	_, msgs2, err := st.Load(string(id))
	if err != nil {
		t.Fatal(err)
	}
	userCount := 0
	for _, m := range msgs2 {
		if m.Role == "user" {
			userCount++
		}
	}
	if userCount != 2 {
		t.Errorf("user messages after 2 turns = %d, want 2 (incremental save duplicated?)", userCount)
	}
}

func TestLoadSessionReplaysHistory(t *testing.T) {
	st := testStore(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "f.txt")
	args, err := json.Marshal(map[string]string{"path": target, "content": "v1"})
	if err != nil {
		t.Fatal(err)
	}

	srv1 := scriptServer(t, []step{
		{toolName: "write", toolArgs: string(args)},
		{text: "written"},
	})
	f1 := newFixture(t, nil, st, factoryFor(srv1, tools.All()))
	f1.initialize(t)
	id := f1.newSession(t, dir)
	if _, err := f1.prompt(t, id, "make the file"); err != nil {
		t.Fatal(err)
	}

	client2 := &fakeClient{}
	srv2 := scriptServer(t, []step{{text: "follow-up done"}})
	f2 := newFixture(t, client2, st, factoryFor(srv2, nil))
	init2, err := f2.conn.Initialize(context.Background(), acp.InitializeRequest{
		ProtocolVersion:    acp.ProtocolVersionNumber,
		ClientCapabilities: acp.ClientCapabilities{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !init2.AgentCapabilities.LoadSession {
		t.Fatal("loadSession should be advertised with a store")
	}
	if init2.AgentCapabilities.SessionCapabilities.Resume == nil {
		t.Fatal("resume should be advertised with a store")
	}

	_, err = f2.conn.LoadSession(context.Background(), acp.LoadSessionRequest{
		SessionId:  id,
		Cwd:        dir,
		McpServers: []acp.McpServer{},
	})
	if err != nil {
		t.Fatalf("session/load: %v", err)
	}

	ups := client2.snapshot()
	var kinds []string
	for _, n := range ups {
		kinds = append(kinds, updateKind(n.Update))
	}
	joined := strings.Join(kinds, " ")
	if !strings.Contains(joined, "user_chunk") {
		t.Errorf("no user chunk in replay: %s", joined)
	}
	if !strings.Contains(joined, "tool_call(") || !strings.Contains(joined, "completed") {
		t.Errorf("no completed tool card in replay: %s", joined)
	}
	if !strings.HasSuffix(joined, "agent_chunk") {
		t.Errorf("replay should end with the assistant reply: %s", joined)
	}

	iu := strings.Index(joined, "user_chunk")
	it := strings.Index(joined, "tool_call(")
	ia := strings.LastIndex(joined, "agent_chunk")
	if iu >= it || it >= ia {
		t.Errorf("replay out of order: %s", joined)
	}

	if _, err := f2.prompt(t, id, "what did you do?"); err != nil {
		t.Fatal(err)
	}
	s := f2.bridge.getSession(id)
	var users []string
	for _, m := range s.ag.MessagesSnapshot() {
		if m.Role == "user" {
			users = append(users, m.Content)
		}
	}
	if len(users) != 2 || users[0] != "make the file" || users[1] != "what did you do?" {
		t.Errorf("conversation = %v", users)
	}
}

func TestLoadSessionUnknown(t *testing.T) {
	st := testStore(t)
	srv := scriptServer(t, []step{{text: "ok"}})
	f := newFixture(t, nil, st, factoryFor(srv, nil))
	f.initialize(t)
	_, err := f.conn.LoadSession(context.Background(), acp.LoadSessionRequest{
		SessionId:  "does-not-exist",
		Cwd:        t.TempDir(),
		McpServers: []acp.McpServer{},
	})
	if err == nil {
		t.Fatal("expected error loading unknown session")
	}
	if !strings.Contains(err.Error(), "-32002") && !strings.Contains(strings.ToLower(err.Error()), "not found") {
		t.Errorf("want resource-not-found, got: %v", err)
	}
}

func TestListSessions(t *testing.T) {
	st := testStore(t)
	srv := scriptServer(t, []step{{text: "ok"}})
	f := newFixture(t, nil, st, factoryFor(srv, nil))
	f.initialize(t)
	dir := t.TempDir()
	id := f.newSession(t, dir)
	if _, err := f.prompt(t, id, "title me"); err != nil {
		t.Fatal(err)
	}

	resp, err := f.conn.ListSessions(context.Background(), acp.ListSessionsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Sessions) == 0 {
		t.Fatal("no sessions listed")
	}
	var found *acp.SessionInfo
	for i := range resp.Sessions {
		if resp.Sessions[i].SessionId == id {
			found = &resp.Sessions[i]
		}
	}
	if found == nil {
		t.Fatalf("created session %q not in list: %+v", id, resp.Sessions)
	}
	if found.Cwd != dir {
		t.Errorf("cwd = %q, want %q", found.Cwd, dir)
	}
	if found.Title == nil || !strings.Contains(*found.Title, "title me") {
		t.Errorf("title = %v", found.Title)
	}

	resp2, err := f.conn.ListSessions(context.Background(), acp.ListSessionsRequest{Cwd: new("/elsewhere")})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range resp2.Sessions {
		if s.SessionId == id {
			t.Error("cwd filter did not exclude session")
		}
	}
}

func TestUsageAndTitleUpdates(t *testing.T) {
	st := testStore(t)
	srv := scriptServer(t, []step{{text: "hi there"}})
	f := newFixture(t, nil, st, func(_ context.Context, cwd string, _ map[string]mcp.ServerConfig) (*agent.Agent, *mcp.Manager, error) {
		ag, mgr, err := factoryFor(srv, nil)(context.Background(), cwd, nil)
		if err != nil {
			return nil, nil, err
		}
		ag.ContextLimit = 100_000
		return ag, mgr, nil
	})
	f.initialize(t)
	id := f.newSession(t, t.TempDir())
	if _, err := f.prompt(t, id, "usage please"); err != nil {
		t.Fatal(err)
	}
	f.client.waitFor(t, func(n acp.SessionNotification) bool {
		return n.Update.UsageUpdate != nil && n.Update.UsageUpdate.Size == 100_000
	}, "usage_update")
	f.client.waitFor(t, func(n acp.SessionNotification) bool {
		u := n.Update.SessionInfoUpdate
		return u != nil && u.Title != nil && strings.Contains(*u.Title, "usage please")
	}, "session_info_update with title")
}

func TestStoreFromSkipsSystemPrompt(t *testing.T) {
	st := testStore(t)
	srv := scriptServer(t, []step{{text: "hi"}})
	f := newFixture(t, nil, st, factoryFor(srv, nil))
	f.initialize(t)
	id := f.newSession(t, t.TempDir())
	if _, err := f.prompt(t, id, "hello"); err != nil {
		t.Fatal(err)
	}

	_, msgs, err := st.Load(string(id))
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) == 0 {
		t.Fatal("nothing persisted")
	}
	if msgs[0].Role == "system" {
		t.Fatalf("system prompt persisted as row 0: %q", truncateStr(msgs[0].Content, 80))
	}
	if msgs[0].Role != "user" || msgs[0].Content != "hello" {
		t.Errorf("first stored message = %s %q", msgs[0].Role, truncateStr(msgs[0].Content, 40))
	}
}

func truncateStr(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func TestLoadSessionRejectsPrefixID(t *testing.T) {
	st := testStore(t)
	srv := scriptServer(t, []step{{text: "ok"}})
	f := newFixture(t, nil, st, factoryFor(srv, nil))
	f.initialize(t)
	id := f.newSession(t, t.TempDir())
	if _, err := f.prompt(t, id, "x"); err != nil {
		t.Fatal(err)
	}

	prefix := string(id)[:4]
	_, err := f.conn.LoadSession(context.Background(), acp.LoadSessionRequest{
		SessionId:  acp.SessionId(prefix),
		Cwd:        "",
		McpServers: []acp.McpServer{},
	})
	if err == nil {
		t.Fatal("prefix id accepted — client would address a session it can't find")
	}
}
