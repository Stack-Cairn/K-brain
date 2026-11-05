package backend

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
)

// pollClient runs a read-only command as a Bash session (yield_time_ms) and then waits
// for it with ProcessWait, the way models poll a command that has not finished.
type pollClient struct {
	scriptedClient
	lock sync.Mutex
	step int
}

var bashSessionID = regexp.MustCompile(`session_id=(\S+)`)

func (c *pollClient) Stream(_ context.Context, req ai.Request, text, _ func(string), _ func(string, string, string)) (ai.Message, ai.Usage, error) {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.step++
	call := func(name string, args any) ai.Message {
		raw, _ := json.Marshal(args)
		tc := ai.ToolCall{ID: "poll-call-" + name, Type: "function"}
		tc.Function.Name, tc.Function.Arguments = name, string(raw)
		return ai.Message{Role: "assistant", ToolCalls: []ai.ToolCall{tc}}
	}
	switch c.step {
	case 1:
		return call("Bash", map[string]any{"command": "echo poll-read-only", "yield_time_ms": 50}), ai.Usage{}, nil
	case 2:
		// The Bash result may already be followed by the session's exit notice,
		// steered in when the command ended after its result was taken.
		var result ai.Message
		for i := len(req.Messages) - 1; i >= 0; i-- {
			if m := req.Messages[i]; m.Role == "tool" && m.ToolCallID == "poll-call-Bash" {
				result = m
				break
			}
		}
		match := bashSessionID.FindStringSubmatch(result.Content)
		if match == nil {
			return ai.Message{Role: "assistant", Content: "no session id in " + result.Content, StopReason: ai.StopReasonStop}, ai.Usage{}, nil
		}
		return call("ProcessWait", map[string]any{"session_id": match[1], "yield_time_ms": 2000}), ai.Usage{}, nil
	}
	if text != nil {
		text("done")
	}
	return ai.Message{Role: "assistant", Content: "done", StopReason: ai.StopReasonStop}, ai.Usage{}, nil
}

func TestPollOfOwnBashSessionRunsWithoutPrompt(t *testing.T) {
	base, _ := newRulesServer(t, &pollClient{})
	sess := createTestSession(t, base)
	events, prompts := runRules(t, base, sess.ID, "normal", func(p protocol.PermissionRequest) protocol.PermissionDecision {
		return protocol.PermissionDecision{Decision: "reject"}
	})
	if len(prompts) != 0 {
		t.Fatalf("waiting on the session's own read-only command must not prompt: %+v", prompts)
	}
	results := toolResults(t, events)
	if len(results) != 2 || results[0].Failed || results[1].Failed || !strings.Contains(results[1].Output, "poll-read-only") {
		t.Fatalf("results=%+v", results)
	}
	var tools []string
	for _, auto := range autoResolved(events) {
		if auto.Source != "read_only" {
			t.Fatalf("auto_resolved=%+v", auto)
		}
		tools = append(tools, auto.Tool)
	}
	if strings.Join(tools, ",") != "Bash,ProcessWait" {
		t.Fatalf("auto-resolved tools=%v, want Bash then ProcessWait", tools)
	}
}
