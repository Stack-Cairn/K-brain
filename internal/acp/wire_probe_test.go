package acp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

func TestWirePromptAfterIdleCancel(t *testing.T) {
	srv := scriptServer(t, []step{{text: "ok"}})

	agentR, probeW, _ := os.Pipe()
	probeR, agentW, _ := os.Pipe()
	b := NewBridge("test", factoryFor(srv, nil), nil, false, nil)
	conn := acp.NewAgentSideConnection(b, agentW, agentR)
	b.SetAgentConnection(conn)

	send := func(v any) {
		fmt.Fprintln(probeW, mustJSON(v))
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
	time.Sleep(50 * time.Millisecond)
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
