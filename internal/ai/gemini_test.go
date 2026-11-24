package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestGeminiStreamNormalizesTextThinkingToolsAndUsage(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models/gemini-test:streamGenerateContent" || r.URL.Query().Get("key") != "secret" {
			t.Fatalf("request = %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"thought\":true,\"text\":\"plan\"}]}}],\"usageMetadata\":{\"promptTokenCount\":7}}\n\n")
		fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"hello\"},{\"functionCall\":{\"id\":\"native-call\",\"name\":\"read_file\",\"args\":{\"path\":\"a.txt\"},\"thoughtSignature\":\"sig-1\"}}]}}],\"usageMetadata\":{\"candidatesTokenCount\":4,\"thoughtsTokenCount\":2}}\n\n")
		fmt.Fprint(w, "data: {\"candidates\":[{\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":7,\"candidatesTokenCount\":4,\"thoughtsTokenCount\":2}}\n\n")
	}))
	defer srv.Close()

	client := NewGemini(srv.URL, "secret")
	var text, thinking string
	var toolID, toolName, toolArgs string
	message, usage, err := client.Stream(context.Background(), Request{
		Model:    "gemini-test",
		Messages: []Message{{Role: "user", Content: "inspect"}},
		Tools:    []Tool{NewTool("read_file", "read", `{"type":"object","properties":{"path":{"type":"string"}}}`)},
	}, func(s string) { text += s }, func(s string) { thinking += s }, func(id, name, args string) { toolID, toolName, toolArgs = id, name, args })
	if err != nil {
		t.Fatal(err)
	}
	if text != "hello" || thinking != "plan" || message.Content != "hello" {
		t.Fatalf("message/text = %+v %q %q", message, text, thinking)
	}
	if toolID != "native-call" || toolName != "read_file" || toolArgs != `{"path":"a.txt"}` {
		t.Fatalf("tool = %q %q %q", toolID, toolName, toolArgs)
	}
	if usage.PromptTokens != 7 || usage.CompletionTokens != 6 {
		t.Fatalf("usage = %+v", usage)
	}
	contents, ok := got["contents"].([]any)
	if !ok || len(contents) != 1 {
		t.Fatalf("payload contents = %#v", got["contents"])
	}
}

func TestGeminiStreamUsesUniqueIDsAndPersistsThoughtSignatures(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	var requests []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		requests = append(requests, body)
		w.Header().Set("Content-Type", "text/event-stream")
		if len(requests) == 2 {
			contents := body["contents"].([]any)
			encoded, _ := json.Marshal(contents)
			if !strings.Contains(string(encoded), `"thoughtSignature":"sig-1"`) || !strings.Contains(string(encoded), `"functionCall"`) {
				t.Fatalf("second request omitted prior call signature: %s", encoded)
			}
			if !strings.Contains(string(encoded), `"functionResponse":{"id":"`) || !strings.Contains(string(encoded), `"content":"first result"`) {
				t.Fatalf("second request omitted function response ID: %s", encoded)
			}
			fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"functionCall\":{\"name\":\"read_file\",\"args\":{\"path\":\"b.txt\"},\"thoughtSignature\":\"sig-2\"}}]}}]}\n\n")
			fmt.Fprint(w, "data: {\"candidates\":[{\"finishReason\":\"STOP\"}]}\n\n")
			return
		}
		fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"functionCall\":{\"name\":\"read_file\",\"args\":{\"path\":\"a.txt\"},\"thoughtSignature\":\"sig-1\"}}]}}]}\n\n")
		fmt.Fprint(w, "data: {\"candidates\":[{\"finishReason\":\"STOP\"}]}\n\n")
	}))
	defer srv.Close()

	client := NewGemini(srv.URL, "secret")
	first, _, err := client.Stream(context.Background(), Request{Model: "gemini-test", Messages: []Message{{Role: "user", Content: "first"}}}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.ToolCalls) != 1 || !strings.HasPrefix(first.ToolCalls[0].ID, "gemini-call-") {
		t.Fatalf("first tool call = %+v", first.ToolCalls)
	}
	second, _, err := client.Stream(context.Background(), Request{Model: "gemini-test", Messages: []Message{
		{Role: "user", Content: "first"},
		first,
		{Role: "tool", ToolCallID: first.ToolCalls[0].ID, Name: "read_file", Content: "first result"},
		{Role: "user", Content: "second"},
	}}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.ToolCalls) != 1 || second.ToolCalls[0].ID == first.ToolCalls[0].ID {
		t.Fatalf("second tool call reused ID = %+v", second.ToolCalls)
	}
}

func TestGeminiSignatureSurvivesNewClientRestart(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	var requests []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		requests = append(requests, body)
		w.Header().Set("Content-Type", "text/event-stream")
		if len(requests) == 1 {
			fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"functionCall\":{\"name\":\"read_file\",\"args\":{\"path\":\"a\"},\"thoughtSignature\":\"persisted-signature\"}}]}}]}\n\n")
			fmt.Fprint(w, "data: {\"candidates\":[{\"finishReason\":\"STOP\"}]}\n\n")
			return
		}
		encoded, _ := json.Marshal(body["contents"])
		if !strings.Contains(string(encoded), `"thoughtSignature":"persisted-signature"`) {
			t.Errorf("restarted Gemini client did not recover signature: %s", encoded)
		}
		fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"ok\"}]}}]}\n\n")
		fmt.Fprint(w, "data: {\"candidates\":[{\"finishReason\":\"STOP\"}]}\n\n")
	}))
	defer srv.Close()
	firstClient := NewGemini(srv.URL, "secret")
	first, _, err := firstClient.Stream(context.Background(), Request{Model: "gemini-test", Messages: []Message{{Role: "user", Content: "read"}}}, nil, nil, nil)
	if err != nil || len(first.ToolCalls) != 1 {
		t.Fatalf("first response = %+v, %v", first, err)
	}
	secondClient := NewGemini(srv.URL, "secret")
	messages := []Message{
		{Role: "user", Content: "read"},
		first,
		{Role: "tool", ToolCallID: first.ToolCalls[0].ID, Name: "read_file", Content: "result"},
		{Role: "user", Content: "continue"},
	}
	if _, _, err := secondClient.Stream(context.Background(), Request{Model: "gemini-test", Messages: messages}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	path, err := secondClient.signaturePath("gemini-test")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Windows has no Unix mode bits; the cache file itself must still exist.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("signature cache mode = %o, want 600", info.Mode().Perm())
	}
}

func TestGeminiGeneratedIDsAreUnrelatedAcrossClients(t *testing.T) {
	first, err := NewGemini("https://example.test/one", "key").canonicalCallID("")
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewGemini("https://example.test/two", "key").canonicalCallID("")
	if err != nil {
		t.Fatal(err)
	}
	if first == second || !strings.HasPrefix(first, "gemini-call-") || !strings.HasPrefix(second, "gemini-call-") {
		t.Fatalf("generated IDs = %q and %q", first, second)
	}
}

func TestGeminiPayloadConvertsCanonicalHistory(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	call := ToolCall{ID: "c", Type: "function"}
	call.Function.Name = "read_file"
	call.Function.Arguments = `{"path":"a"}`
	client := NewGemini("", "")
	client.signatures["m\x00c"] = "sig-c"
	payload := client.geminiPayloadWithState(Request{Model: "m", Messages: []Message{
		{Role: "system", Content: "rules"},
		{Role: "assistant", Content: "done", ToolCalls: []ToolCall{call}},
		{Role: "tool", ToolCallID: "c", Name: "read_file", Content: "file"},
		{Role: "user", Content: "next"},
	}})
	if _, ok := payload["systemInstruction"]; !ok {
		t.Fatal("missing system instruction")
	}
	contents := payload["contents"].([]map[string]any)
	if len(contents) != 2 || contents[0]["role"] != "model" || contents[1]["role"] != "user" {
		t.Fatalf("contents = %#v", contents)
	}
	encoded, _ := json.Marshal(payload)
	if !strings.Contains(string(encoded), "functionResponse") || !strings.Contains(string(encoded), "functionCall") || !strings.Contains(string(encoded), "sig-c") {
		t.Fatalf("encoded payload = %s", encoded)
	}
}

func TestGeminiStreamHandlesJSONArrayResponses(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, "[{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"thought\":true,\"text\":\"check\"}]}}]},\n")
		fmt.Fprint(w, "{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"hello vertex\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":5,\"candidatesTokenCount\":3,\"thoughtsTokenCount\":2}}]")
	}))
	defer srv.Close()

	var text, thinking string
	msg, usage, err := NewGemini(srv.URL+"/v1", "k").Stream(
		context.Background(),
		Request{Model: "gemini-4-neon", ReasoningEffort: "low", Messages: []Message{{Role: "user", Content: "hi"}}},
		func(s string) { text += s },
		func(s string) { thinking += s },
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content != "hello vertex" || text != "hello vertex" || thinking != "check" {
		t.Fatalf("msg=%+v text=%q thinking=%q", msg, text, thinking)
	}
	if usage.PromptTokens != 5 || usage.CompletionTokens != 5 {
		t.Fatalf("usage=%+v", usage)
	}
}
