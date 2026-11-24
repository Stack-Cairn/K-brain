package ai

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestChatCompatibilityParameters(t *testing.T) {
	for _, tc := range []struct {
		model, effort, key string
		value              any
	}{
		{"gpt-5.3-codex", "max", "reasoning_effort", "xhigh"},
		{"o3", "max", "reasoning_effort", "high"},
		{"o3", "off", "reasoning_effort", "low"},
		{"gpt-5", "off", "reasoning_effort", "minimal"},
		{"gpt-5.3-codex", "off", "reasoning_effort", "low"},
		{"gpt-5.2", "off", "reasoning_effort", "none"},
		{"deepseek-chat", "off", "thinking", map[string]any{"type": "disabled"}},
		{"deepseek-reasoner", "high", "thinking", map[string]any{"type": "enabled"}},
		{"glm-4.7", "off", "thinking", map[string]any{"type": "disabled"}},
		{"kimi-k2.5", "high", "thinking", map[string]any{"type": "enabled"}},
		{"qwen3", "off", "enable_thinking", false},
		{"qwen3", "high", "enable_thinking", true},
		{"grok-4", "high", "reasoning_effort", nil},
	} {
		t.Run(tc.model+tc.effort, func(t *testing.T) {
			body, err := New("http://relay.test/v1", "key").chatBody(Request{Model: tc.model, ReasoningEffort: tc.effort, MaxTokens: 2048})
			if err != nil {
				t.Fatal(err)
			}
			var p map[string]any
			if err = json.Unmarshal(body, &p); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(p[tc.key], tc.value) {
				t.Fatalf("payload=%s", body)
			}
			if openAIReasoningModel(tc.model) && (p["max_completion_tokens"] != float64(2048) || p["max_tokens"] != nil) {
				t.Fatalf("token limit=%s", body)
			}
			if tc.key != "reasoning_effort" && p["reasoning_effort"] != nil {
				t.Fatalf("unsupported effort=%s", body)
			}
		})
	}
}

func TestChatReasoningAliasesReplayAndRepeatedToolName(t *testing.T) {
	for _, field := range []string{"reasoning_content", "reasoning", "reasoning_text"} {
		t.Run(field, func(t *testing.T) {
			var bodies []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				bodies = append(bodies, string(body))
				fmt.Fprint(w, streamFrames(fmt.Sprintf(`{"choices":[{"delta":{%q:"plan"}}]}`, field),
					`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c","function":{"name":"read","arguments":"{"}}]}}]}`,
					`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"read","arguments":"}"}}]}}]}`,
					`{"choices":[{"finish_reason":"tool_calls"}]}`, `[DONE]`))
			}))
			defer srv.Close()
			client := New(srv.URL, "key")
			msg, _, err := client.Stream(t.Context(), Request{Model: "deepseek-chat"}, nil, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].Function.Name != "read" {
				t.Fatalf("call=%+v", msg)
			}
			raw, _ := json.Marshal(msg)
			var saved Message
			if err = json.Unmarshal(raw, &saved); err != nil {
				t.Fatal(err)
			}
			for _, model := range []string{"deepseek-chat", "other"} {
				_, _, err = New(srv.URL, "key").Stream(t.Context(), Request{Model: model, Messages: []Message{saved, {Role: "tool", ToolCallID: "c", Content: "result"}}}, nil, nil, nil)
				if err != nil {
					t.Fatal(err)
				}
			}
			if !strings.Contains(bodies[1], `"reasoning_content":"plan"`) || strings.Contains(bodies[1], "provider_replay") || strings.Contains(bodies[2], "reasoning_content") {
				t.Fatalf("replay=%v", bodies)
			}
		})
	}
}

func TestResponsesEncryptedReplayAndIncludeMerge(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		fmt.Fprint(w, streamFrames(
			`{"type":"response.output_item.done","item":{"type":"reasoning","id":"r","summary":[],"encrypted_content":"old"}}`,
			`{"type":"response.completed","response":{"status":"completed","output":[{"type":"reasoning","id":"r","summary":[],"encrypted_content":"secret"},{"type":"function_call","id":"i","call_id":"c","name":"read","arguments":"{}"}]}}`))
	}))
	defer srv.Close()
	msg, _, err := NewResponses(srv.URL, "key").Stream(t.Context(), Request{Model: "gpt-5.3"}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(msg)
	var saved Message
	if err = json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.Replay.Blocks) != 1 {
		t.Fatalf("duplicate replay=%+v", saved.Replay)
	}
	for _, model := range []string{"gpt-5.3", "grok-4"} {
		client := NewResponses(srv.URL, "key")
		client.SetCacheKey("session-key")
		if model == "grok-4" {
			supported := false
			client.SetCacheOptions(CacheOptions{SupportsKey: &supported})
		}
		_, _, err = client.Stream(t.Context(), Request{Model: model, ReasoningEffort: "high", NativeWebSearch: true, Messages: []Message{saved, {Role: "tool", ToolCallID: "c", Content: "result"}}}, nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	var p map[string]any
	if err = json.Unmarshal([]byte(bodies[1]), &p); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p["include"], []any{"reasoning.encrypted_content", "web_search_call.action.sources"}) {
		t.Fatalf("include=%v", p)
	}
	if !strings.Contains(bodies[1], `"encrypted_content":"secret"`) || strings.Contains(bodies[1], `"encrypted_content":"old"`) {
		t.Fatalf("replay=%s", bodies[1])
	}
	if strings.Contains(bodies[2], "encrypted_content\":\"secret") || strings.Contains(bodies[2], "prompt_cache_key") || strings.Contains(bodies[2], `"reasoning":`) {
		t.Fatalf("xai=%s", bodies[2])
	}
}

func TestGeminiThinkingSchemaAndToolImages(t *testing.T) {
	image := ImagePart("png", pngFixture(t, 2, 2))
	if err := validateGeminiAttachments([]Message{{Role: "tool", Parts: []ContentPart{image}}}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		model, effort, key string
		value              any
	}{
		{"gemini-2.5-flash", "off", "thinkingBudget", 0},
		{"gemini-2.5-flash-lite", "max", "thinkingBudget", -1},
		{"gemini-2.5-pro", "off", "thinkingBudget", 128},
		{"gemini-3-pro", "max", "thinkingLevel", "HIGH"},
		{"gemini-3-pro-preview", "medium", "thinkingLevel", "HIGH"},
		{"gemini-3.1-pro-preview", "medium", "thinkingLevel", "MEDIUM"},
		{"gemini-3-flash", "off", "thinkingLevel", "MINIMAL"},
		{"gemini-3.8-flash", "off", "thinkingLevel", "LOW"},
		{"gemini-4-neon", "off", "thinkingLevel", "LOW"},
		{"gemini-4-neon", "medium", "thinkingLevel", "MEDIUM"},
		{"gemini-4-neon", "xhigh", "thinkingLevel", "XHIGH"},
		{"gemini-4-carbon", "xhigh", "thinkingLevel", "HIGH"},
		{"gemini-pro-latest", "high", "thinkingLevel", "HIGH"},
	} {
		t.Run(tc.model+"_"+tc.effort, func(t *testing.T) {
			p := geminiPayload(Request{Model: tc.model, ReasoningEffort: tc.effort, Tools: []Tool{NewTool("read", "read", `{"type":"object","additionalProperties":false}`)}, Messages: []Message{{Role: "assistant"}, {Role: "tool", Name: "read", ToolCallID: "a", Parts: []ContentPart{image}}, {Role: "tool", Name: "read", ToolCallID: "b", Content: "second"}}})
			thinking := p["generationConfig"].(map[string]any)["thinkingConfig"].(map[string]any)
			if thinking[tc.key] != tc.value {
				t.Fatalf("thinking=%v", thinking)
			}
			contents := p["contents"].([]map[string]any)
			if len(contents) != 1 || len(contents[0]["parts"].([]any)) != 3 {
				t.Fatalf("contents=%v", contents)
			}
			raw, _ := json.Marshal(p)
			if !strings.Contains(string(raw), "parametersJsonSchema") || !strings.Contains(string(raw), "inlineData") {
				t.Fatalf("payload=%s", raw)
			}
		})
	}
	if strings.Contains(geminiPath("https://example.test/v1", "m", true), "v1beta") {
		t.Fatal("duplicated version prefix")
	}
	if got := geminiPath("https://aiplatform.googleapis.com/v1/publishers/google", "gemini-4-neon", true); got != "/models/gemini-4-neon:streamGenerateContent?alt=sse" {
		t.Fatalf("vertex path = %q", got)
	}
	if got := geminiPath("https://api.site/v1beta/models", "gemini-4-hydrogen", true); got != "/gemini-4-hydrogen:streamGenerateContent?alt=sse" {
		t.Fatalf("trailing /models path = %q", got)
	}
	if got := geminiPath("https://relay.example/publishers/team", "gemini-4-neon", true); !strings.HasPrefix(got, "/v1beta/models/") {
		t.Fatalf("custom relay path = %q", got)
	}
	if isOfficialGeminiEndpoint("https://aiplatform.googleapis.com/v1/publishers/google") {
		t.Fatal("Vertex AI should not bypass the native-search + function-tools guard")
	}
	fullURLClient := NewGeminiWithOptions("https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash:streamGenerateContent?alt=sse", "k", true, "", nil)
	fullURLPayload := fullURLClient.geminiPayloadWithState(Request{Model: "fast", ReasoningEffort: "low", Messages: []Message{{Role: "user", Content: "hi"}}})
	if got := fullURLPayload["generationConfig"].(map[string]any)["thinkingConfig"].(map[string]any)["thinkingBudget"]; got != 1024 {
		t.Fatalf("full-URL 2.5 alias thinkingBudget = %v", got)
	}
	opaqueRelayClient := NewGeminiWithOptions("https://relay.example/generate", "k", true, "", nil)
	opaqueRelayPayload := opaqueRelayClient.geminiPayloadWithState(Request{Model: "fast", ReasoningEffort: "low", Messages: []Message{{Role: "user", Content: "hi"}}})
	if got := opaqueRelayPayload["generationConfig"].(map[string]any)["thinkingConfig"].(map[string]any)["thinkingBudget"]; got != 1024 {
		t.Fatalf("opaque relay alias thinkingBudget = %v", got)
	}
	if _, err := NewGemini("https://aiplatform.googleapis.com/v1/publishers/google", "api-key-only").Models(t.Context()); err == nil {
		t.Fatal("expected clear error for API-key-only Vertex Express Mode Models()")
	}
	vertexCatalogPages := 0
	vertexCatalogSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vertexCatalogPages++
		if r.URL.Path != "/v1beta1/publishers/google/models" {
			t.Fatalf("vertex catalog path = %q", r.URL.Path)
		}
		if vertexCatalogPages == 1 {
			fmt.Fprint(w, `{"publisherModels":[{"name":"publishers/google/models/gemini-4-neon"}],"nextPageToken":"p2"}`)
			return
		}
		if r.URL.Query().Get("pageToken") != "p2" {
			t.Fatalf("expected pageToken=p2, got %q", r.URL.RawQuery)
		}
		fmt.Fprint(w, `{"publisherModels":[{"name":"publishers/google/models/gemini-4-hydrogen"}]}`)
	}))
	defer vertexCatalogSrv.Close()
	discovered, err := NewGeminiWithOptions(vertexCatalogSrv.URL, "", false, vertexCatalogSrv.URL+"/v1beta1/publishers/google/models", map[string]string{"Authorization": "Bearer token"}).Models(t.Context())
	if err != nil || len(discovered) != 2 || discovered[0].ID != "gemini-4-neon" || discovered[1].ID != "gemini-4-hydrogen" {
		t.Fatalf("vertex publisherModels = %+v, err=%v", discovered, err)
	}
}

func TestBlockedProviderResponsesDoNotBecomeSuccessfulMessages(t *testing.T) {
	for _, tc := range []struct {
		name, wire string
		responses  bool
	}{
		{"gemini-prompt", `{"promptFeedback":{"blockReason":"SAFETY"}}`, false},
		{"gemini-candidate", `{"candidates":[{"content":{"parts":[{"text":"partial"}]},"finishReason":"SAFETY"}]}`, false},
		{"gemini-malformed-call", `{"candidates":[{"finishReason":"MALFORMED_FUNCTION_CALL"}]}`, false},
		{"gemini-error", `{"error":{"message":"failed"}}`, false},
		{"responses-refusal", `{"status":"completed","output":[{"type":"message","content":[{"type":"refusal","refusal":"declined"}]}]}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.Header.Get("Accept"), "event-stream") {
					wire := tc.wire
					if tc.responses {
						wire = `{"type":"response.completed","response":` + wire + `}`
					}
					fmt.Fprint(w, streamFrames(wire))
				} else {
					fmt.Fprint(w, tc.wire)
				}
			}))
			defer srv.Close()
			var client Client = NewGemini(srv.URL, "key")
			if tc.responses {
				client = NewResponses(srv.URL, "key")
			}
			if text, _, err := client.Complete(t.Context(), Request{Model: "test"}); err == nil || text != "" {
				t.Fatalf("complete text=%q error=%v", text, err)
			}
			if msg, _, err := client.Stream(t.Context(), Request{Model: "test"}, nil, nil, nil); err == nil || msg.Content != "" || msg.Replay != nil {
				t.Fatalf("stream message=%+v error=%v", msg, err)
			}
		})
	}
}
