package backend

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

const (
	previewChart = "<!doctype html><html><head><title>Sales &amp; chart</title></head><body><canvas></canvas><script>draw()</script></body></html>\n"
	previewPage  = "<!doctype html><html><body><h1>Site</h1></body></html>"
	previewTail  = "<p>tail</p>\n"
)

// previewModel answers with an "html preview" block streamed in pieces, a
// plain html block, then writes site/index.html, then ends with a preview
// block whose closing fence has no newline.
type previewModel struct {
	scriptedClient
	mu      sync.Mutex
	prompts []string
}

func (c *previewModel) Clone() ai.Client { return c }
func (c *previewModel) Stream(_ context.Context, request ai.Request, text, _ func(string), _ func(string, string, string)) (ai.Message, ai.Usage, error) {
	c.mu.Lock()
	c.prompts = append(c.prompts, request.Messages[0].Content)
	c.mu.Unlock()
	if !strings.Contains(request.Messages[0].Content, "html preview") {
		text("plain")
		return ai.Message{Role: "assistant", Content: "plain", StopReason: ai.StopReasonStop}, ai.Usage{}, nil
	}
	for _, message := range request.Messages {
		if message.Role == "tool" {
			answer := "Again:\n~~~~ html  preview\n" + previewTail + "~~~~"
			for _, piece := range []string{answer[:10], answer[10:]} {
				text(piece)
			}
			return ai.Message{Role: "assistant", Content: answer, StopReason: ai.StopReasonStop}, ai.Usage{}, nil
		}
	}
	answer := "Here:\n```html preview\n" + previewChart + "```\n\nCode only:\n```html\n<b>not a preview</b>\n```\n" +
		"```html preview\n```\n" // empty: ignored
	for i := 0; i < len(answer); i += 7 {
		text(answer[i:min(i+7, len(answer))])
	}
	call := ai.ToolCall{ID: "write-page", Type: "function"}
	args, _ := json.Marshal(map[string]string{"path": "site/index.html", "content": previewPage})
	call.Function.Name, call.Function.Arguments = "write", string(args)
	return ai.Message{Role: "assistant", Content: answer, ToolCalls: []ai.ToolCall{call}, StopReason: ai.StopReasonToolUse}, ai.Usage{}, nil
}

func previewFixture(t *testing.T) (*httptest.Server, *previewModel, string, string) {
	t.Helper()
	root := t.TempDir()
	store, err := session.Open(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	model := &previewModel{}
	eventDir := filepath.Join(root, "events")
	service, err := New(Options{Store: store, EventDir: eventDir, MemoryRoot: filepath.Join(root, "memory"), DefaultCWD: root,
		Models: []protocol.ModelRef{{Provider: "fixture", Model: "fixture-model"}},
		Factory: func(_ context.Context, _ string, m protocol.ModelRef) (*agent.Agent, error) {
			return agent.New(model, m.Model, 4096, "system", agent.WithSystemPromptResolver(func() (string, error) { return "system", nil })), nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(service)
	t.Cleanup(func() { server.Close(); service.Close(); store.Close() })
	return server, model, root, eventDir
}

func runPreviewTurn(t *testing.T, server *httptest.Server, sessionID, prompt string, previews bool) []protocol.PreviewCreated {
	t.Helper()
	var run protocol.RunAccepted
	resp := postJSON(t, http.DefaultClient, server.URL+"/v1/sessions/"+sessionID+"/runs", protocol.PromptRequest{ConversationID: sessionID, ClientRequestID: prompt, Prompt: prompt,
		Options: &protocol.RunOptions{PermissionMode: "full_access", HTMLPreviews: previews}}, &run)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("run status %d", resp.StatusCode)
	}
	waitForRunTerminal(t, server.URL, sessionID, run.RunID)
	var out []protocol.PreviewCreated
	for _, event := range waitForRun(t, server.URL, sessionID, run.AcceptedSeq-1) {
		if event.Type != protocol.EventPreviewCreated || event.RunID != run.RunID {
			continue
		}
		var preview protocol.PreviewCreated
		if err := json.Unmarshal(event.Payload, &preview); err != nil {
			t.Fatal(err)
		}
		out = append(out, preview)
	}
	return out
}

func TestHTMLPreviewsFromFencedBlocksAndWrittenFiles(t *testing.T) {
	server, model, root, eventDir := previewFixture(t)
	var sess protocol.Session
	resp := postJSON(t, http.DefaultClient, server.URL+"/v1/sessions", protocol.CreateSessionRequest{CWD: root, Model: protocol.ModelRef{Provider: "fixture", Model: "fixture-model"}}, &sess)
	resp.Body.Close()

	// Without the option: no hint, no previews.
	if got := runPreviewTurn(t, server, sess.ID, "first", false); len(got) != 0 {
		t.Fatalf("previews without the option: %+v", got)
	}
	if strings.Contains(model.prompts[0], "html preview") {
		t.Fatalf("hint leaked into a run without html_previews: %q", model.prompts[0])
	}

	previews := runPreviewTurn(t, server, sess.ID, "second", true)
	if len(previews) != 3 {
		t.Fatalf("previews = %+v", previews)
	}
	chart, page, tail := previews[0], previews[1], previews[2]
	if chart.Source != "message" || chart.Title != "Sales & chart" || chart.Bytes != len(previewChart) || len(chart.PreviewID) != 16 ||
		chart.URL != "/v1/sessions/"+sess.ID+"/previews/"+chart.PreviewID || chart.CreatedAt.IsZero() {
		t.Fatalf("chart preview = %+v", chart)
	}
	if page.Source != "file" || page.Path != "site/index.html" || page.ToolCallID != "write-page" || page.Title != "index.html" {
		t.Fatalf("file preview = %+v", page)
	}
	if tail.Source != "message" || tail.Bytes != len(previewTail) {
		t.Fatalf("tail preview = %+v", tail)
	}
	if data, err := os.ReadFile(filepath.Join(root, "site", "index.html")); err != nil || string(data) != previewPage {
		t.Fatalf("written file = %q %v", data, err)
	}
	if last := model.prompts[len(model.prompts)-1]; !strings.HasPrefix(last, "system") || !strings.Contains(last, "```html preview") {
		t.Fatalf("hint missing: %q", last)
	}

	resp, err := http.Get(server.URL + chart.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	csp := resp.Header.Get("Content-Security-Policy")
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(csp, "sandbox allow-scripts; default-src 'none';") || strings.Contains(csp, "allow-same-origin") ||
		!strings.Contains(csp, "connect-src 'none'") || resp.Header.Get("Content-Type") != "text/html; charset=utf-8" || resp.Header.Get("Set-Cookie") != "" {
		t.Fatalf("preview response %d %v", resp.StatusCode, resp.Header)
	}
	if !strings.Contains(string(body), `<head><meta http-equiv="Content-Security-Policy"`) || !strings.Contains(string(body), "<script>draw()</script>") {
		t.Fatalf("preview body = %s", body)
	}

	resp, err = http.Get(server.URL + "/v1/sessions/" + sess.ID + "/previews")
	if err != nil {
		t.Fatal(err)
	}
	var listing struct {
		Previews []protocol.PreviewCreated `json:"previews"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&listing)
	resp.Body.Close()
	if len(listing.Previews) != 3 {
		t.Fatalf("listing = %+v", listing)
	}

	for _, path := range []string{"/previews/0000000000000000", "/previews/..%2f..%2fx", "/previews/" + strings.ToUpper(chart.PreviewID), "/previews/" + chart.PreviewID + "/x"} {
		resp, err := http.Get(server.URL + "/v1/sessions/" + sess.ID + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d", path, resp.StatusCode)
		}
	}

	// Deleting the session removes its previews.
	req, _ := http.NewRequest(http.MethodDelete, server.URL+"/v1/sessions/"+sess.ID, nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(eventDir, "previews", sess.ID)); !os.IsNotExist(err) {
		t.Fatalf("previews kept after delete: %v", err)
	}
}

func TestPreviewFenceScanner(t *testing.T) {
	var f previewFenceScanner
	for _, piece := range []string{"intro\n``", "` html preview\n<p>a</p>\n````\n", "   ```html preview\nb\n", "``` not closing\n", "c\n```"} {
		f.write(piece)
	}
	f.flush()
	if len(f.pending) != 2 || f.pending[0] != "<p>a</p>\n" || f.pending[1] != "b\n``` not closing\nc\n" {
		t.Fatalf("blocks = %q", f.pending)
	}
	f.pending = nil
	// An unclosed block, an indented-code fence and a too-large block are dropped.
	f.write("```html preview\nunclosed")
	f.flush()
	f.write("    ```html preview\nx\n```\n")
	f.write("```html preview\n" + strings.Repeat("x", maxPreviewBytes) + "\n```\n")
	f.write("```html preview\nsmall\n```\n")
	if len(f.pending) != 1 || f.pending[0] != "small\n" {
		t.Fatalf("blocks = %d %q", len(f.pending), f.pending[0][:min(20, len(f.pending[0]))])
	}
}
