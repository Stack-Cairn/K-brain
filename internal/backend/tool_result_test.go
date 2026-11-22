package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// tool.result carries the tool's text once, in output; content holds only
// the attachments (images) the tool added, never a copy of the text.
// Clients read output as the text and content for images.

func TestToolResultContentHoldsOnlyAttachments(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nfake")
	imageTool := tools.Tool{Def: ai.NewTool("snap", "Attach an image.", `{"type":"object"}`), Run: func(ctx context.Context, _ json.RawMessage) (string, error) {
		tools.AttachImage(ctx, ".png", png)
		return "captured", nil
	}}
	textTool := tools.Tool{Def: ai.NewTool("say", "Say something.", `{"type":"object"}`), Run: func(context.Context, json.RawMessage) (string, error) {
		return "hello", nil
	}}
	list := []tools.Tool{imageTool, textTool}

	result := tools.ExecuteResult(context.Background(), list, "snap", json.RawMessage(`{}`), true)
	content := protocol.FromAIMessage(ai.Message{Role: "tool", Parts: result.Parts}).Content
	if result.Text != "captured" || len(content) != 1 || content[0].Type != protocol.ContentImage {
		t.Fatalf("image tool: text %q content %+v", result.Text, content)
	}
	result = tools.ExecuteResult(context.Background(), list, "say", json.RawMessage(`{}`), true)
	if content := protocol.FromAIMessage(ai.Message{Role: "tool", Parts: result.Parts}).Content; result.Text != "hello" || len(content) != 0 {
		t.Fatalf("text tool: text %q content %+v", result.Text, content)
	}
}

func TestToolResultEventSendsTextOnlyAsOutput(t *testing.T) {
	server, _, root, _ := previewFixture(t)
	var sess protocol.Session
	resp := postJSON(t, http.DefaultClient, server.URL+"/v1/sessions", protocol.CreateSessionRequest{CWD: root, Model: protocol.ModelRef{Provider: "fixture", Model: "fixture-model"}}, &sess)
	resp.Body.Close()
	// The preview model writes site/index.html in its first step.
	runPreviewTurn(t, server, sess.ID, "write", true)
	for _, event := range waitForRun(t, server.URL, sess.ID, 0) {
		if event.Type != protocol.EventToolResult {
			continue
		}
		var raw struct {
			ToolResult map[string]json.RawMessage `json:"tool_result"`
		}
		if err := json.Unmarshal(event.Payload, &raw); err != nil {
			t.Fatal(err)
		}
		if _, ok := raw.ToolResult["content"]; ok || len(raw.ToolResult["output"]) < 3 {
			t.Fatalf("tool.result = %s", event.Payload)
		}
		return
	}
	t.Fatal("no tool.result")
}
