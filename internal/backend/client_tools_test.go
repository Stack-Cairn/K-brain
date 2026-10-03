package backend

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

const browserClientToolSchema = `{"type":"object","properties":{"action":{"type":"string"},"url":{"type":"string"}},"required":["action"]}`

type clientToolModel struct{ scriptedClient }

func (c *clientToolModel) Clone() ai.Client { return c }
func (c *clientToolModel) Stream(_ context.Context, request ai.Request, text, _ func(string), _ func(string, string, string)) (ai.Message, ai.Usage, error) {
	for _, message := range request.Messages {
		if message.Role == "tool" && message.Name == "Browser" {
			if !strings.Contains(message.Content, "Page: Example Domain") {
				return ai.Message{}, ai.Usage{}, fmt.Errorf("client result not delivered: %q", message.Content)
			}
			text("done")
			return ai.Message{Role: "assistant", Content: "done", StopReason: ai.StopReasonStop}, ai.Usage{}, nil
		}
	}
	offered := false
	for _, tool := range request.Tools {
		offered = offered || tool.Function.Name == "Browser"
	}
	if !offered {
		return ai.Message{}, ai.Usage{}, fmt.Errorf("client tool was not offered to the model")
	}
	call := ai.ToolCall{ID: "browser-call", Type: "function"}
	call.Function.Name, call.Function.Arguments = "Browser", `{"action":"navigate","url":"https://example.com"}`
	return ai.Message{Role: "assistant", ToolCalls: []ai.ToolCall{call}, StopReason: ai.StopReasonToolUse}, ai.Usage{}, nil
}

func clientToolFixture(t *testing.T) *httptest.Server {
	t.Helper()
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	backend, err := New(Options{Store: store, EventDir: t.TempDir(), MemoryRoot: t.TempDir(), Factory: func(_ context.Context, _ string, m protocol.ModelRef) (*agent.Agent, error) {
		return agent.New(&clientToolModel{}, m.Model, 1024, "system"), nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(backend)
	t.Cleanup(func() { backend.Close(); server.Close(); store.Close() })
	return server
}

func TestClientToolCallRoundTripsThroughTheClient(t *testing.T) {
	server := clientToolFixture(t)
	sess := createTestSession(t, server.URL)
	var run protocol.RunAccepted
	resp := postJSON(t, http.DefaultClient, server.URL+"/v1/sessions/"+sess.ID+"/runs", protocol.PromptRequest{
		ConversationID: sess.ID, ClientRequestID: "client-tool-run", Prompt: "open example.com",
		Options: &protocol.RunOptions{
			ClientTools: []protocol.ClientTool{{Name: "Browser", Description: "Drive the desktop browser.", Parameters: json.RawMessage(browserClientToolSchema)}},
			Tools:       &protocol.ToolSelection{Policies: map[string]string{"Browser": "allow"}},
		},
	}, &run)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("run status %d", resp.StatusCode)
	}
	events, err := http.Get(server.URL + "/v1/sessions/" + sess.ID + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer events.Body.Close()
	scanner := bufio.NewScanner(events.Body)
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<20)
	var resolved, completed bool
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event protocol.Event
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			t.Fatal(err)
		}
		switch event.Type {
		case protocol.EventClientToolRequested:
			var request protocol.ClientToolRequest
			if err := json.Unmarshal(event.Payload, &request); err != nil {
				t.Fatal(err)
			}
			if request.Tool != "Browser" || request.RunID != run.RunID || request.ToolCallID != "browser-call" || !strings.Contains(string(request.Arguments), "example.com") {
				t.Fatalf("client tool request = %+v", request)
			}
			path := server.URL + "/v1/sessions/" + sess.ID + "/client-tools/" + request.CallID
			wrong := postJSON(t, http.DefaultClient, path, protocol.ClientToolResultRequest{ConversationID: sess.ID, RunID: "other-run", Text: "x"}, nil)
			wrong.Body.Close()
			if wrong.StatusCode != http.StatusConflict {
				t.Fatalf("mismatched run status %d", wrong.StatusCode)
			}
			image := protocol.ClientToolImage{MimeType: "image/png", Data: base64.StdEncoding.EncodeToString([]byte("not-really-a-png"))}
			ok := postJSON(t, http.DefaultClient, path, protocol.ClientToolResultRequest{ConversationID: sess.ID, RunID: run.RunID, Text: "Page: Example Domain", Images: []protocol.ClientToolImage{image}}, nil)
			ok.Body.Close()
			if ok.StatusCode != http.StatusOK {
				t.Fatalf("result status %d", ok.StatusCode)
			}
			again := postJSON(t, http.DefaultClient, path, protocol.ClientToolResultRequest{ConversationID: sess.ID, RunID: run.RunID, Text: "again"}, nil)
			again.Body.Close()
			if again.StatusCode != http.StatusConflict {
				t.Fatalf("second result status %d", again.StatusCode)
			}
		case protocol.EventClientToolResolved:
			var result protocol.ClientToolResolution
			if err := json.Unmarshal(event.Payload, &result); err != nil {
				t.Fatal(err)
			}
			if result.Text != "Page: Example Domain" || result.ImageCount != 1 || result.IsError {
				t.Fatalf("resolution = %+v", result)
			}
			resolved = true
		case protocol.EventRunFailed:
			t.Fatalf("run failed: %s", event.Payload)
		case protocol.EventRunCompleted:
			completed = true
		}
		if completed {
			break
		}
	}
	if !resolved || !completed {
		t.Fatalf("resolved=%v completed=%v", resolved, completed)
	}
}

func TestClientToolOptionsValidation(t *testing.T) {
	root := t.TempDir()
	available := []tools.Tool{{Def: ai.NewTool("Read", "read", `{"type":"object"}`)}}
	schema := json.RawMessage(`{"type":"object"}`)
	got, err := normalizeRunOptions(&protocol.RunOptions{
		ClientTools: []protocol.ClientTool{{Name: "Browser", Description: "browser", Parameters: schema}},
		Tools:       &protocol.ToolSelection{Policies: map[string]string{"Browser": "ask"}},
	}, root, available)
	if err != nil || len(got.ClientTools) != 1 || got.Tools.Policies["Browser"] != "ask" {
		t.Fatalf("declared client tool policy rejected: %+v err=%v", got, err)
	}
	for name, tool := range map[string]protocol.ClientTool{
		"collision":    {Name: "Read", Description: "x", Parameters: schema},
		"bad name":     {Name: "bad name", Description: "x", Parameters: schema},
		"no desc":      {Name: "Browser", Parameters: schema},
		"array schema": {Name: "Browser", Description: "x", Parameters: json.RawMessage(`[]`)},
	} {
		if _, err := normalizeRunOptions(&protocol.RunOptions{ClientTools: []protocol.ClientTool{tool}}, root, available); err == nil {
			t.Fatalf("%s: invalid client tool accepted", name)
		}
	}
	if _, err := normalizeRunOptions(&protocol.RunOptions{Tools: &protocol.ToolSelection{Policies: map[string]string{"Browser": "allow"}}}, root, available); err == nil {
		t.Fatal("policy for an undeclared tool was accepted")
	}
}
