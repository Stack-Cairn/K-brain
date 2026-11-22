package backend

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/mcp"
	"github.com/Stack-Cairn/K-brain/internal/mcp/mcptest"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

// appsClient calls each tool in calls once, in order, then answers "done".
type appsClient struct {
	scriptedClient
	calls []string
}

func (c *appsClient) Clone() ai.Client { return c }
func (c *appsClient) Stream(_ context.Context, request ai.Request, text, _ func(string), _ func(string, string, string)) (ai.Message, ai.Usage, error) {
	done := 0
	for _, message := range request.Messages {
		if message.Role == "tool" {
			done++
		}
	}
	if done >= len(c.calls) {
		text("done")
		return ai.Message{Role: "assistant", Content: "done", StopReason: ai.StopReasonStop}, ai.Usage{}, nil
	}
	call := ai.ToolCall{ID: "apps-call-" + c.calls[done], Type: "function"}
	call.Function.Name, call.Function.Arguments = c.calls[done], `{}`
	return ai.Message{Role: "assistant", ToolCalls: []ai.ToolCall{call}, StopReason: ai.StopReasonToolUse}, ai.Usage{}, nil
}

func appsFixture(t *testing.T, calls ...string) (*httptest.Server, string) {
	t.Helper()
	isolateUserDir(t)
	root := t.TempDir()
	store, err := session.Open(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	live, err := mcp.NewLiveManager(context.Background(), filepath.Join(root, "mcp.json"), mcp.LiveSettings{Servers: []mcp.LiveServer{{
		ID: "apps", Enabled: true, Transport: "stdio", Command: exe, Env: map[string]string{mcptest.AppsServerEnv: "1"},
		Policy: map[string]string{"mcp__apps__secret": "deny"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for len(live.ToolsFor(context.Background(), root, nil)) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("MCP not ready: %+v", live.Statuses())
		}
		time.Sleep(10 * time.Millisecond)
	}
	service, err := New(Options{Store: store, EventDir: filepath.Join(root, "events"), MemoryRoot: filepath.Join(root, "memory"), DefaultCWD: root, MCP: live,
		Models: []protocol.ModelRef{{Provider: "fixture", Model: "fixture-model"}},
		Factory: func(_ context.Context, _ string, m protocol.ModelRef) (*agent.Agent, error) {
			return agent.New(&appsClient{calls: calls}, m.Model, 4096, "system"), nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(service)
	t.Cleanup(func() { server.Close(); service.Close(); live.Close(); store.Close() })
	return server, root
}

func runAndCollectToolResults(t *testing.T, server *httptest.Server, root string) (protocol.Session, map[string]protocol.ToolResult) {
	t.Helper()
	var sess protocol.Session
	resp := postJSON(t, http.DefaultClient, server.URL+"/v1/sessions", protocol.CreateSessionRequest{CWD: root, Model: protocol.ModelRef{Provider: "fixture", Model: "fixture-model"}}, &sess)
	resp.Body.Close()
	var run protocol.RunAccepted
	resp = postJSON(t, http.DefaultClient, server.URL+"/v1/sessions/"+sess.ID+"/runs", protocol.PromptRequest{ConversationID: sess.ID, ClientRequestID: "apps", Prompt: "show the board",
		Options: &protocol.RunOptions{PermissionMode: "full_access"}}, &run)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("run status %d", resp.StatusCode)
	}
	waitForRunTerminal(t, server.URL, sess.ID, run.RunID)
	results := map[string]protocol.ToolResult{}
	for _, event := range waitForRun(t, server.URL, sess.ID, 0) {
		if event.Type != protocol.EventToolResult {
			continue
		}
		var payload protocol.ToolResultEvent
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		results[payload.ToolResult.Name] = payload.ToolResult
	}
	return sess, results
}

func TestMCPAppToolResultAnnouncesAndServesItsView(t *testing.T) {
	server, root := appsFixture(t, "mcp__apps__board", "mcp__apps__plain", "mcp__apps__secret")
	sess, results := runAndCollectToolResults(t, server, root)
	board := results["mcp__apps__board"]
	if board.Output != "board ready" || board.UIResource == nil || board.UIResource.ServerID != "apps" || board.UIResource.URI != "ui://board/view.html" {
		t.Fatalf("board result = %+v", board)
	}
	wantURL := "/v1/sessions/" + sess.ID + "/mcp-ui?" + url.Values{"server": {"apps"}, "uri": {"ui://board/view.html"}}.Encode()
	if board.UIResource.URL != wantURL {
		t.Fatalf("ui url = %q, want %q", board.UIResource.URL, wantURL)
	}
	if plain := results["mcp__apps__plain"]; plain.UIResource != nil {
		t.Fatalf("tool without a UI announced one: %+v", plain)
	}
	if secret := results["mcp__apps__secret"]; !secret.Failed || secret.UIResource != nil {
		t.Fatalf("denied tool = %+v", secret)
	}

	resp, err := http.Get(server.URL + board.UIResource.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	csp := resp.Header.Get("Content-Security-Policy")
	html := string(body)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/html; charset=utf-8" ||
		!strings.HasPrefix(csp, "sandbox allow-scripts; default-src 'none';") || strings.Contains(csp, "allow-same-origin") ||
		!strings.Contains(csp, "connect-src https://api.example.com;") || !strings.Contains(csp, "script-src 'unsafe-inline' https://cdn.example.com;") ||
		strings.Contains(csp, "insecure") || resp.Header.Get("X-Content-Type-Options") != "nosniff" || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("mcp-ui: %d headers %v", resp.StatusCode, resp.Header)
	}
	if !strings.HasPrefix(html, `<!doctype html><html><head><meta http-equiv="Content-Security-Policy" content="default-src 'none';`) || !strings.Contains(html, "<div id=app>board</div>") {
		t.Fatalf("mcp-ui body = %s", html)
	}

	get := func(query string) int {
		t.Helper()
		resp, err := http.Get(server.URL + "/v1/sessions/" + sess.ID + "/mcp-ui?" + query)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	for query, want := range map[string]int{
		"server=apps&uri=" + url.QueryEscape("ui://secret/view.html"):     http.StatusNotFound,
		"server=apps&uri=" + url.QueryEscape("ui://undeclared/view.html"): http.StatusNotFound,
		"server=other&uri=" + url.QueryEscape("ui://board/view.html"):     http.StatusNotFound,
		"server=apps&uri=" + url.QueryEscape("file:///etc/passwd"):        http.StatusBadRequest,
		"server=apps&uri=" + url.QueryEscape("ui://notes/view.txt"):       http.StatusBadGateway,
	} {
		if got := get(query); got != want {
			t.Errorf("%s: %d, want %d", query, got, want)
		}
	}

	// The MCP tool listing marks the tool with its view.
	resp, err = http.Get(server.URL + "/v1/mcp/tools?cwd=" + url.QueryEscape(root))
	if err != nil {
		t.Fatal(err)
	}
	var listing struct {
		Tools []map[string]any `json:"tools"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&listing)
	resp.Body.Close()
	marked := map[string]any{}
	for _, tool := range listing.Tools {
		marked[tool["name"].(string)] = tool["uiResourceUri"]
	}
	if marked["mcp__apps__board"] != "ui://board/view.html" || marked["mcp__apps__legacy"] != "ui://legacy/view.html" || marked["mcp__apps__plain"] != nil || marked["mcp__apps__secret"] != nil {
		t.Fatalf("tool listing ui marks = %v", marked)
	}
}

func TestPolicyMetaGoesFirstInTheDocument(t *testing.T) {
	policy := sandboxPolicy(nil, nil)
	meta := `<meta http-equiv="Content-Security-Policy" content="` + policy + `">`
	for in, want := range map[string]string{
		`<html><HEAD lang="x"><title>t</title></HEAD></html>`: `<html><HEAD lang="x">` + meta + `<title>t</title></HEAD></html>`,
		`<!DOCTYPE html><p>x</p>`:                             `<!DOCTYPE html>` + meta + `<p>x</p>`,
		`<p>fragment</p>`:                                     meta + `<p>fragment</p>`,
	} {
		if got := withPolicyMeta(in, policy); got != want {
			t.Errorf("withPolicyMeta(%q) = %q", in, got)
		}
	}
	if strings.Contains(policy, "sandbox") || !strings.Contains(policy, "connect-src 'none'") {
		t.Fatalf("policy = %q", policy)
	}
}
