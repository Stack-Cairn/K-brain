package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
)

// callsClient answers the first user turn with the given tool calls, then "done".
type callsClient struct {
	scriptedClient
	lock  sync.Mutex
	calls [][2]string
	sent  bool
}

func (c *callsClient) Stream(_ context.Context, req ai.Request, text, _ func(string), _ func(string, string, string)) (ai.Message, ai.Usage, error) {
	c.lock.Lock()
	defer c.lock.Unlock()
	if last := req.Messages[len(req.Messages)-1]; last.Role != "tool" && !c.sent {
		c.sent = true
		var calls []ai.ToolCall
		for i, nameArgs := range c.calls {
			call := ai.ToolCall{ID: fmt.Sprintf("meta-call-%d", i+1), Type: "function"}
			call.Function.Name, call.Function.Arguments = nameArgs[0], nameArgs[1]
			calls = append(calls, call)
		}
		return ai.Message{Role: "assistant", ToolCalls: calls}, ai.Usage{}, nil
	}
	if text != nil {
		text("done")
	}
	return ai.Message{Role: "assistant", Content: "done", StopReason: ai.StopReasonStop}, ai.Usage{}, nil
}

func TestToolResultEventAndHistoryCarryExitCodeAndDuration(t *testing.T) {
	base, _ := newRulesServer(t, &callsClient{calls: [][2]string{{"Bash", `{"command":"exit 5"}`}}})
	sess := createTestSession(t, base)
	events, _ := runRules(t, base, sess.ID, "normal", func(protocol.PermissionRequest) protocol.PermissionDecision {
		return protocol.PermissionDecision{Decision: "allow_once"}
	})
	var raw string
	for _, event := range events {
		if event.Type == protocol.EventToolResult {
			raw = string(event.Payload)
		}
	}
	results := toolResults(t, events)
	if len(results) != 1 {
		t.Fatalf("results=%+v", results)
	}
	r := results[0]
	if !r.Failed || r.ExitCode == nil || *r.ExitCode != 5 || r.DurationMs < 1 || r.TimedOut {
		t.Fatalf("tool.result=%s", raw)
	}

	resp, err := http.Get(base + "/v1/sessions/" + sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var session protocol.Session
	if err := json.Unmarshal(body, &session); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, m := range session.Messages {
		for _, call := range m.ToolCalls {
			if call.ID == "meta-call-1" {
				found = true
				if call.ExitCode == nil || *call.ExitCode != 5 || call.DurationMs < 1 {
					t.Fatalf("history tool call: %+v", call)
				}
			}
		}
	}
	if !found {
		t.Fatalf("tool call missing from history: %s", body)
	}
}

func TestPermissionRequestedCarriesEditPreview(t *testing.T) {
	base, _ := newRulesServer(t, &callsClient{calls: [][2]string{{"Write", `{"path":"note.txt","content":"hello\nworld\n"}`}}})
	sess := createTestSession(t, base)
	events, prompts := runRules(t, base, sess.ID, "normal", func(protocol.PermissionRequest) protocol.PermissionDecision {
		return protocol.PermissionDecision{Decision: "reject"}
	})
	if len(prompts) != 1 {
		t.Fatalf("prompts=%+v", prompts)
	}
	p := prompts[0].Preview
	if p == nil || !strings.HasSuffix(p.Path, "note.txt") || !p.NewFile || p.Diff != "1 + hello\n2 + world" || p.Truncated {
		t.Fatalf("preview=%+v", p)
	}
	for _, event := range events {
		if event.Type == protocol.EventPermissionRequest {
			redacted := redactTrajectoryEvents([]protocol.Event{event})
			if strings.Contains(string(redacted[0].Payload), "preview") || strings.Contains(string(redacted[0].Payload), "hello") {
				t.Fatalf("redacted trajectory kept the preview: %s", redacted[0].Payload)
			}
		}
	}
}
