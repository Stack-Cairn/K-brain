package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

type idleCancelObserver struct {
	*Bridge
	cancelled chan struct{}
}

func (o *idleCancelObserver) Cancel(ctx context.Context, params acp.CancelNotification) error {
	err := o.Bridge.Cancel(ctx, params)
	close(o.cancelled)
	return err
}

func TestWirePromptAfterIdleCancel(t *testing.T) {
	srv := scriptServer(t, []step{{text: "ok"}})

	agentR, probeW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = agentR.Close(); _ = probeW.Close() })
	probeR, agentW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = probeR.Close(); _ = agentW.Close() })
	b := NewBridge("test", factoryFor(srv, nil), nil, false, nil)
	t.Cleanup(b.CloseAll)
	observer := &idleCancelObserver{Bridge: b, cancelled: make(chan struct{})}
	conn := acp.NewAgentSideConnection(observer, agentW, agentR)
	b.SetAgentConnection(conn)

	send := func(v any) {
		if _, err := fmt.Fprintln(probeW, mustJSON(v)); err != nil {
			t.Fatal(err)
		}
	}
	readLine := make(chan string, 32)
	go func() {
		sc := bufio.NewScanner(probeR)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			readLine <- sc.Text()
		}
	}()
	awaitResp := func(id int) map[string]any {
		t.Helper()
		deadline := time.After(5 * time.Second)
		for {
			select {
			case l := <-readLine:
				var m map[string]any
				if json.Unmarshal([]byte(l), &m) != nil {
					continue
				}
				if mid, ok := m["id"].(float64); ok && int(mid) == id {
					return m
				}
			case <-deadline:
				t.Fatalf("no response to id %d", id)
			}
		}
	}

	send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": 1}})
	awaitResp(1)
	send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "session/new", "params": map[string]any{"cwd": t.TempDir(), "mcpServers": []any{}}})
	sessResp := awaitResp(2)
	sid := sessResp["result"].(map[string]any)["sessionId"].(string)

	send(map[string]any{"jsonrpc": "2.0", "method": "session/cancel", "params": map[string]any{"sessionId": sid}})
	select {
	case <-observer.cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("idle cancellation was not processed")
	}
	send(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "session/prompt", "params": map[string]any{"sessionId": sid, "prompt": []any{map[string]any{"type": "text", "text": "hi"}}}})
	promptResp := awaitResp(3)
	result := promptResp["result"].(map[string]any)
	t.Logf("prompt response: %s", mustJSON(promptResp))
	if result["stopReason"] != "end_turn" {
		t.Errorf("stopReason = %v, want end_turn — SDK synthesized %v?", result["stopReason"], result["stopReason"])
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
