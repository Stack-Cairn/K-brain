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
		for _, field := range []string{"exit_code", "timed_out", "duration_ms", "preview", "sandbox_denial"} {
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
	got = marshalString(t, ToolResult{ID: "c1", Output: "x", ToolRunInfo: ToolRunInfo{ExitCode: new(1), SandboxDenial: &SandboxDenial{Kind: "write", Path: "/w/x", SuggestedRoot: "/w"}}})
	if got != `{"id":"c1","output":"x","exit_code":1,"sandbox_denial":{"kind":"write","path":"/w/x","suggested_root":"/w"}}` {
		t.Fatalf("sandbox denial: %s", got)
	}
	if got = marshalString(t, SandboxDenial{Kind: "network"}); got != `{"kind":"network"}` {
		t.Fatalf("network denial: %s", got)
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

// A stored MCP App reference becomes the call's ui_resource in history, with
// no URL (the backend adds the session's paths) and no inline result.
func TestFromAIMessageKeepsMCPAppView(t *testing.T) {
	call := ai.ToolCall{ID: "c1", Type: "function", MCPApp: &ai.MCPAppRef{ServerID: "apps", Tool: "chart", URI: "ui://board/view.html", ResultStored: true}}
	call.Function.Name, call.Function.Arguments = "mcp__apps__chart", "{}"
	other := ai.ToolCall{ID: "c2", Type: "function", MCPApp: &ai.MCPAppRef{ServerID: "apps", Tool: "x", URI: "https://example.invalid/x"}}
	other.Function.Name, other.Function.Arguments = "mcp__apps__x", "{}"
	msg := FromAIMessage(ai.Message{Role: "assistant", ToolCalls: []ai.ToolCall{call, other}})
	ui := msg.ToolCalls[0].UIResource
	if ui == nil || ui.ServerID != "apps" || ui.URI != "ui://board/view.html" || ui.ToolName != "chart" || !ui.ResultStored || ui.URL != "" || ui.Result != nil {
		t.Fatalf("ui_resource = %+v", ui)
	}
	if msg.ToolCalls[1].UIResource != nil {
		t.Fatalf("non-ui:// resource kept: %+v", msg.ToolCalls[1].UIResource)
	}
	got := marshalString(t, msg.ToolCalls[0])
	if strings.Contains(got, "ResultStored") || strings.Contains(got, `"result"`) || !strings.Contains(got, `"ui_resource":{"server_id":"apps","uri":"ui://board/view.html","url":"","tool_name":"chart"}`) {
		t.Fatalf("tool call json = %s", got)
	}
}
