package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func TestConcurrentSessionsKeepPermissionModesSeparate(t *testing.T) {
	asked := make(chan struct{})
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	firstServer := scriptServer(t, []step{{toolName: "authorize", toolArgs: `{}`}, {text: "done"}})
	secondServer := scriptServer(t, []step{{toolName: "authorize", toolArgs: `{}`}, {text: "done"}})
	probe := tools.Tool{Def: llmTool("authorize"), Run: func(ctx context.Context, _ json.RawMessage) (string, error) {
		return "checked", tools.Authorize(ctx, "write", "note.txt")
	}}
	client := &fakeClient{answer: func(p acp.RequestPermissionRequest) permAnswer {
		close(asked)
		<-release
		return permAnswer{optionID: optAllowOnce}
	}}
	first := newFixture(t, client, nil, factoryFor(firstServer, []tools.Tool{probe}))
	second := newFixture(t, nil, nil, factoryFor(secondServer, []tools.Tool{probe}))
	t.Cleanup(unblock)
	first.initialize(t)
	second.initialize(t)
	id1 := first.newSession(t, t.TempDir())
	id2 := second.newSession(t, t.TempDir())
	if _, err := first.conn.SetSessionMode(context.Background(), acp.SetSessionModeRequest{SessionId: id1, ModeId: ModeAsk}); err != nil {
		t.Fatal(err)
	}
	run := func(f *fixture, id acp.SessionId) <-chan error {
		done := make(chan error, 1)
		go func() {
			resp, err := f.conn.Prompt(context.Background(), acp.PromptRequest{SessionId: id, Prompt: []acp.ContentBlock{acp.TextBlock("run")}})
			if err == nil && resp.StopReason != acp.StopReasonEndTurn {
				err = fmt.Errorf("stop reason = %v", resp.StopReason)
			}
			done <- err
		}()
		return done
	}
	done1 := run(first, id1)
	select {
	case <-asked:
	case <-time.After(3 * time.Second):
		t.Fatal("ask session did not request permission")
	}
	done2 := run(second, id2)
	select {
	case err := <-done2:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("auto session blocked behind another session's permission")
	}
	second.client.mu.Lock()
	count := len(second.client.perms)
	second.client.mu.Unlock()
	if count != 0 {
		t.Fatalf("auto session requested permission %d times", count)
	}
	unblock()
	select {
	case err := <-done1:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ask session did not finish")
	}
}
