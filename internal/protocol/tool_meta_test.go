package protocol

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func marshalString(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestToolRunInfoAndPreviewOmittedWhenEmpty(t *testing.T) {
	for _, v := range []any{
		ToolResult{ID: "c1", Name: "read", Output: "x"},
		ToolCall{ID: "c1", Name: "read", Arguments: json.RawMessage(`{}`)},
		PermissionRequest{PermissionID: "p", Tool: "bash", Command: "ls"},
	} {
		got := marshalString(t, v)
		for _, field := range []string{"exit_code", "timed_out", "duration_ms", "preview"} {
			if strings.Contains(got, field) {
				t.Errorf("%T has %s when empty: %s", v, field, got)
			}
		}
	}
}

func TestToolRunInfoJSON(t *testing.T) {
	got := marshalString(t, ToolResult{ID: "c1", Name: "bash", Output: "ok", ToolRunInfo: ToolRunInfo{ExitCode: new(0), DurationMs: 12}})
	if got != `{"id":"c1","name":"bash","output":"ok","exit_code":0,"duration_ms":12}` {
		t.Fatalf("tool result: %s", got)
	}
	got = marshalString(t, ToolResult{ID: "c1", Output: "", Failed: true, ToolRunInfo: ToolRunInfo{TimedOut: true, DurationMs: 3}})
	if got != `{"id":"c1","output":"","failed":true,"timed_out":true,"duration_ms":3}` {
		t.Fatalf("timed out result: %s", got)
	}
	var back ToolResult
	if err := json.Unmarshal([]byte(`{"id":"c1","output":"ok","exit_code":2,"duration_ms":5}`), &back); err != nil || back.ExitCode == nil || *back.ExitCode != 2 || back.DurationMs != 5 {
		t.Fatalf("decode: %+v %v", back, err)
	}
	got = marshalString(t, PermissionRequest{PermissionID: "p", Tool: "edit", Command: "/f", Preview: &PermissionPreview{Path: "/f", Diff: "1 - a\n1 + b"}})
	if !strings.Contains(got, `"preview":{"path":"/f","diff":"1 - a\n1 + b"}`) {
		t.Fatalf("permission preview: %s", got)
	}
}

func TestHistoryToolCallCarriesRunInfo(t *testing.T) {
	tc := ai.ToolCall{ID: "c1", DurationMs: 40, ExitCode: 1, ExitStatus: new(1)}
	tc.Function.Name, tc.Function.Arguments = "bash", `{"command":"false"}`
	plain := ai.ToolCall{ID: "c2"}
	plain.Function.Name, plain.Function.Arguments = "read", `{}`
	msg := FromAIMessage(ai.Message{Role: RoleAssistant, ToolCalls: []ai.ToolCall{tc, plain}})
	got := marshalString(t, msg.ToolCalls)
	if got != `[{"id":"c1","name":"bash","arguments":{"command":"false"},"exit_code":1,"duration_ms":40},{"id":"c2","name":"read","arguments":{}}]` {
		t.Fatalf("history tool calls: %s", got)
	}
	// Run info is display metadata: it does not travel back into a model request.
	back, err := msg.ToAIMessage()
	if err != nil {
		t.Fatal(err)
	}
	if c := back.ToolCalls[0]; c.ExitStatus != nil || c.DurationMs != 0 {
		t.Fatalf("round trip kept run info: %+v", c)
	}
}
