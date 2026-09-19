package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func TestPlanModeBlocksWritesAndRestoresAuto(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "result.txt")
	args, err := json.Marshal(map[string]string{"path": target, "content": "written"})
	if err != nil {
		t.Fatal(err)
	}
	requests := make(chan ai.Request, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ai.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		requests <- req
		w.Header().Set("Content-Type", "text/event-stream")
		if req.Messages[len(req.Messages)-1].Role == "user" {
			encoded, _ := json.Marshal(string(args))
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"write-file\",\"type\":\"function\",\"function\":{\"name\":\"write\",\"arguments\":%s}}]}}]}\n\n", encoded)
		} else {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"}}]}\n\n")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	f := newFixture(t, nil, nil, factoryFor(srv, nil))
	f.initialize(t)
	id := f.newSession(t, dir)
	setMode := func(mode acp.SessionModeId) {
		t.Helper()
		if _, err := f.conn.SetSessionMode(context.Background(), acp.SetSessionModeRequest{SessionId: id, ModeId: mode}); err != nil {
			t.Fatal(err)
		}
	}
	setMode(ModePlan)
	if _, err := f.prompt(t, id, "write a file"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("plan wrote a file: %v", err)
	}
	planned := <-requests
	<-requests
	if !strings.Contains(planned.Messages[0].Content, "Plan mode is active") {
		t.Fatal("model did not receive plan instructions")
	}
	seenRead := false
	for _, tool := range planned.Tools {
		switch tool.Function.Name {
		case "read":
			seenRead = true
		case "question", "todowrite":
		default:
			t.Fatalf("plan exposed %s", tool.Function.Name)
		}
	}
	if !seenRead {
		t.Fatal("plan did not expose read")
	}
	if strings.Contains(f.bridge.getSession(id).ag.MessagesSnapshot()[0].Content, "Plan mode is active") {
		t.Fatal("plan instructions leaked into persisted history")
	}
	setMode(ModeAuto)
	if _, err := f.prompt(t, id, "implement now"); err != nil {
		t.Fatal(err)
	}
	implemented := <-requests
	if strings.Contains(implemented.Messages[0].Content, "Plan mode is active") {
		t.Fatal("leaving plan retained instructions")
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "written" {
		t.Fatalf("auto mode did not restore write: %q, %v", data, err)
	}
}

func TestModeChangeAppliesDuringRunningTurn(t *testing.T) {
	for _, mode := range []acp.SessionModeId{ModeAsk, ModePlan} {
		t.Run(string(mode), func(t *testing.T) {
			started := make(chan struct{})
			release := make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			ran := false
			probe := tools.Tool{Def: llmTool("probe"), Run: func(ctx context.Context, _ json.RawMessage) (string, error) {
				close(started)
				select {
				case <-release:
				case <-ctx.Done():
					return "", ctx.Err()
				}
				if err := tools.Authorize(ctx, "write", "note.txt"); err != nil {
					return "", err
				}
				ran = true
				return "wrote", nil
			}}
			srv := scriptServer(t, []step{{toolName: "probe", toolArgs: `{}`}, {text: "finished"}})
			client := &fakeClient{answer: func(acp.RequestPermissionRequest) permAnswer { return permAnswer{optionID: optReject} }}
			f := newFixture(t, client, nil, factoryFor(srv, []tools.Tool{probe}))
			t.Cleanup(unblock)
			f.initialize(t)
			id := f.newSession(t, t.TempDir())
			done := make(chan error, 1)
			go func() { _, err := f.prompt(t, id, "run"); done <- err }()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("tool did not start")
			}
			if _, err := f.conn.SetSessionMode(context.Background(), acp.SetSessionModeRequest{SessionId: id, ModeId: mode}); err != nil {
				t.Fatal(err)
			}
			unblock()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("turn did not finish")
			}
			if ran {
				t.Fatal("tool ignored new mode")
			}
			client.mu.Lock()
			count := len(client.perms)
			client.mu.Unlock()
			want := 0
			if mode == ModeAsk {
				want = 1
			}
			if count != want {
				t.Fatalf("permission count = %d, want %d", count, want)
			}
		})
	}
}

func TestPlanModeOverridesPendingPermission(t *testing.T) {
	requested := make(chan struct{})
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	ran := false
	client := &fakeClient{answer: func(acp.RequestPermissionRequest) permAnswer {
		close(requested)
		<-release
		return permAnswer{optionID: optAllowOnce}
	}}
	srv := scriptServer(t, []step{{toolName: "probe", toolArgs: `{}`}, {text: "done"}})
	f := newFixture(t, client, nil, factoryFor(srv, []tools.Tool{gatedProbe(&ran)}))
	t.Cleanup(unblock)
	f.initialize(t)
	id := f.newSession(t, t.TempDir())
	if _, err := f.conn.SetSessionMode(context.Background(), acp.SetSessionModeRequest{SessionId: id, ModeId: ModeAsk}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := f.prompt(t, id, "run"); done <- err }()
	select {
	case <-requested:
	case <-time.After(3 * time.Second):
		t.Fatal("permission was not requested")
	}
	if _, err := f.conn.SetSessionMode(context.Background(), acp.SetSessionModeRequest{SessionId: id, ModeId: ModePlan}); err != nil {
		t.Fatal(err)
	}
	unblock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("turn did not finish")
	}
	if ran {
		t.Fatal("stale permission allowed write after switching to plan")
	}
}

func TestPlanModeReadsProjectFilesAndUpdatesPlan(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("project note"), 0600); err != nil {
		t.Fatal(err)
	}
	srv := scriptServer(t, []step{
		{toolName: "read", toolArgs: `{"path":"note.txt"}`},
		{toolName: "todowrite", toolArgs: `{"todos":[{"content":"Review project note","status":"in_progress"}]}`},
		{text: "Here is the plan"},
	})
	f := newFixture(t, nil, nil, factoryFor(srv, nil))
	f.initialize(t)
	id := f.newSession(t, dir)
	if _, err := f.conn.SetSessionMode(context.Background(), acp.SetSessionModeRequest{SessionId: id, ModeId: ModePlan}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.prompt(t, id, "inspect and plan"); err != nil {
		t.Fatal(err)
	}
	read := false
	for _, msg := range f.bridge.getSession(id).ag.MessagesSnapshot() {
		if msg.Role == "tool" && strings.Contains(msg.Content, "project note") {
			read = true
		}
	}
	if !read {
		t.Fatal("plan could not read a file relative to its project")
	}
	f.client.waitFor(t, func(n acp.SessionNotification) bool {
		return n.Update.Plan != nil && len(n.Update.Plan.Entries) == 1 && n.Update.Plan.Entries[0].Content == "Review project note"
	}, "plan update")
}
