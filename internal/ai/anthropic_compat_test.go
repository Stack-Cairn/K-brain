package ai

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAnthropicThinkingCompatibility(t *testing.T) {
	for _, tc := range []struct {
		model, effort, kind, output string
		max, budget                 int
	}{
		{"claude-opus-5-5", "high", "adaptive", "high", 4096, 0},
		{"claude-sonnet-4-6", "xhigh", "adaptive", "max", 4096, 0},
		{"claude-opus-4-7", "xhigh", "adaptive", "xhigh", 4096, 0},
		{"claude-4.6-sonnet", "minimal", "adaptive", "low", 4096, 0},
		{"claude-sonnet-4-20250514", "high", "enabled", "", 4096, 4095},
		{"claude-3-5-sonnet", "minimal", "enabled", "", 4096, 1024},
		{"claude-opus-4-1", "max", "enabled", "", 8192, 8191},
		{"claude-sonnet-4", "high", "enabled", "", 0, 4095},
		{"claude-sonnet-4", "high", "", "", 1024, 0},
		{"claude-opus-5-5", "off", "disabled", "", 4096, 0},
		{"claude-opus-5-5", "none", "disabled", "", 4096, 0},
	} {
		t.Run(tc.model+"/"+tc.effort, func(t *testing.T) {
			p, err := anthropicPayload(Request{Model: tc.model, ReasoningEffort: tc.effort, MaxTokens: tc.max}, true)
			if err != nil {
				t.Fatal(err)
			}
			thinking, _ := p["thinking"].(map[string]any)
			if tc.kind == "" {
				if thinking != nil {
					t.Fatalf("unexpected thinking: %v", p)
				}
				return
			}
			if thinking["type"] != tc.kind {
				t.Fatalf("thinking=%v", thinking)
			}
			if tc.budget > 0 && thinking["budget_tokens"] != tc.budget {
				t.Fatalf("budget=%v", thinking)
			}
			if tc.output != "" && p["output_config"].(map[string]any)["effort"] != tc.output {
				t.Fatalf("effort=%v", p)
			}
		})
	}
}

func TestAnthropicSignedReplaySurvivesRestartAndIsScoped(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		fmt.Fprint(w, streamFrames(
			`{"type":"message_start","message":{}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"plan"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"signed"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"redacted_thinking","data":"opaque"}}`,
			`{"type":"content_block_stop","index":1}`,
			`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"call","name":"read","input":{}}}`,
			`{"type":"content_block_stop","index":2}`,
			`{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`,
			`{"type":"message_stop"}`,
		))
	}))
	defer srv.Close()
	msg, _, err := NewAnthropic(srv.URL, "key").Stream(t.Context(), Request{Model: "claude-opus-5-5"}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	var restored Message
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	history := []Message{restored, {Role: "tool", ToolCallID: "call", Content: "result"}}
	for _, model := range []string{"claude-opus-5-5", "other-model"} {
		_, _, err = NewAnthropic(srv.URL, "key").Stream(t.Context(), Request{Model: model, Messages: history}, nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(bodies[1], `"signature":"signed"`) || !strings.Contains(bodies[1], `"data":"opaque"`) {
		t.Fatalf("missing replay: %s", bodies[1])
	}
	if strings.Contains(bodies[2], "signed") || strings.Contains(bodies[2], "opaque") {
		t.Fatalf("cross-model replay: %s", bodies[2])
	}
	restored.Replay.Endpoint = "other-endpoint"
	_, _, err = NewAnthropic(srv.URL, "key").Stream(t.Context(), Request{Model: "claude-opus-5-5", Messages: []Message{restored}}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(bodies[3], "signed") {
		t.Fatalf("cross-endpoint replay: %s", bodies[3])
	}
}

func TestAnthropicServerToolDeltaDoesNotBecomeClientTool(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, streamFrames(
			`{"type":"message_start","message":{}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"search","name":"web_search","input":{}}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"test\"}"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":"answer"}}`,
			`{"type":"content_block_stop","index":1}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
			`{"type":"message_stop"}`,
		))
	}))
	defer srv.Close()
	msg, _, err := NewAnthropic(srv.URL, "key").Stream(t.Context(), Request{Model: "m", NativeWebSearch: true}, nil, nil, func(id, name, args string) { t.Error("server tool exposed to client") })
	if err != nil || msg.Content != "answer" || len(msg.ToolCalls) != 0 {
		t.Fatalf("message=%+v error=%v", msg, err)
	}
}

// A client tool that shares a native tool's name (the CLI's web_fetch) is still the
// client's to run while native web search is on; an undeclared one is bridged.
func TestAnthropicClientToolNamedLikeNativeFetchStaysClientTool(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, streamFrames(
			`{"type":"message_start","message":{}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call","name":"web_fetch","input":{}}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"url\":\"https://example.test\"}"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`,
			`{"type":"message_stop"}`,
		))
	}))
	defer srv.Close()
	fetch := NewTool("web_fetch", "fetch", `{"type":"object","properties":{"url":{"type":"string"}}}`)
	var notified []string
	msg, _, err := NewAnthropic(srv.URL, "key").Stream(t.Context(), Request{Model: "m", NativeWebSearch: true, Tools: []Tool{fetch}}, nil, nil, func(id, name, args string) { notified = append(notified, name) })
	if err != nil || len(msg.ToolCalls) != 1 || msg.ToolCalls[0].Function.Name != "web_fetch" || len(notified) != 1 {
		t.Fatalf("declared web_fetch: message=%+v notified=%v error=%v", msg, notified, err)
	}
	msg, _, err = NewAnthropic(srv.URL, "key").Stream(t.Context(), Request{Model: "m", NativeWebSearch: true}, nil, nil, nil)
	if err != nil || len(msg.ToolCalls) != 0 || !strings.Contains(msg.Content, "did not execute the provider-native web_fetch") {
		t.Fatalf("undeclared web_fetch: message=%+v error=%v", msg, err)
	}
}
