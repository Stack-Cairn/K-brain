package backend

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/mcp"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func getRaw(t *testing.T, url string) (int, []byte) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

// The tool.result of an MCP App tool carries the server's raw result
// (structuredContent, _meta) for the view; history keeps a reference to it.
func TestMCPAppToolResultCarriesRawResult(t *testing.T) {
	big := maxInlineToolResultBytes + 1024
	server, root := appsFixture(t, "mcp__apps__chart", `mcp__apps__big {"bytes":`+jsonNumber(big)+`}`, "mcp__apps__plain")
	sess, results := runAndCollectToolResults(t, server, root)
	chart := results["mcp__apps__chart"].UIResource
	if chart == nil || chart.ToolName != "chart" || chart.ServerID != "apps" {
		t.Fatalf("chart ui = %+v", chart)
	}
	var raw struct {
		Meta              map[string]any `json:"_meta"`
		StructuredContent map[string]any `json:"structuredContent"`
	}
	if err := json.Unmarshal(chart.Result, &raw); err != nil || raw.StructuredContent["title"] != "Sales" || raw.Meta["fake/trace"] != "t-1" {
		t.Fatalf("inline result = %s (%v)", chart.Result, err)
	}
	wantPrefix := "/v1/sessions/" + sess.ID + "/mcp-ui/results/"
	if !strings.HasPrefix(chart.ResultURL, wantPrefix) {
		t.Fatalf("result_url = %q", chart.ResultURL)
	}
	status, body := getRaw(t, server.URL+chart.ResultURL)
	var stored storedMCPResult
	if status != http.StatusOK || json.Unmarshal(body, &stored) != nil || stored.ToolCallID != "apps-call-mcp__apps__chart" || stored.ServerID != "apps" || stored.ToolName != "chart" || stored.URI != "ui://board/view.html" || !bytes.Equal(stored.Result, chart.Result) {
		t.Fatalf("stored result %d: %s", status, body)
	}

	// A large result is only stored, not sent inline.
	bigUI := results["mcp__apps__big"].UIResource
	if bigUI == nil || bigUI.Result != nil || bigUI.ResultURL == "" {
		t.Fatalf("big ui = %+v", bigUI)
	}
	if status, body := getRaw(t, server.URL+bigUI.ResultURL); status != http.StatusOK || len(body) < big {
		t.Fatalf("big stored result %d, %d bytes", status, len(body))
	}
	if plain := results["mcp__apps__plain"]; plain.UIResource != nil {
		t.Fatalf("plain = %+v", plain)
	}

	// History keeps the view on the tool call, with its paths and no inline result.
	status, body = getRaw(t, server.URL+"/v1/sessions/"+sess.ID)
	if status != http.StatusOK || strings.Contains(string(body), `"structuredContent"`) {
		t.Fatalf("session %d leaks raw results: %.300s", status, body)
	}
	var view protocol.Session
	_ = json.Unmarshal(body, &view)
	found := map[string]*protocol.ToolUIResource{}
	for _, msg := range view.Messages {
		for _, call := range msg.ToolCalls {
			found[call.Name] = call.UIResource
		}
	}
	if got := found["mcp__apps__chart"]; got == nil || got.URL != chart.URL || got.ResultURL != chart.ResultURL || got.ToolName != "chart" || got.Result != nil {
		t.Fatalf("history chart view = %+v", got)
	}
	if got := found["mcp__apps__plain"]; got != nil {
		t.Fatalf("history plain view = %+v", got)
	}
	var page struct {
		Session protocol.Session `json:"session"`
	}
	status, body = getRaw(t, server.URL+"/v1/sessions/"+sess.ID+"/history")
	if status != http.StatusOK || json.Unmarshal(body, &page) != nil {
		t.Fatalf("history %d: %.300s", status, body)
	}
	historyHas := false
	for _, msg := range page.Session.Messages {
		for _, call := range msg.ToolCalls {
			if call.Name == "mcp__apps__chart" && call.UIResource != nil && call.UIResource.ResultURL == chart.ResultURL {
				historyHas = true
			}
		}
	}
	if !historyHas {
		t.Fatalf("history route lacks the view: %.500s", body)
	}

	for path, want := range map[string]int{
		wantPrefix + "nothex":                             http.StatusNotFound,
		wantPrefix + strings.Repeat("0", 32):              http.StatusNotFound,
		"/v1/sessions/missing/mcp-ui/results/" + "0":      http.StatusNotFound,
		strings.Replace(chart.ResultURL, sess.ID, "x", 1): http.StatusNotFound,
	} {
		if status, _ := getRaw(t, server.URL+path); status != want {
			t.Errorf("GET %s = %d, want %d", path, status, want)
		}
	}

	// A public share shows no views: their routes need the owner's token.
	var share protocol.ShareStatus
	if code := doJSON(t, http.MethodPost, server.URL+"/v1/sessions/"+sess.ID+"/share", protocol.ShareUpdateRequest{Enabled: true, RedactToolContent: new(false)}, &share); code != http.StatusOK || share.Token == "" {
		t.Fatalf("share %d %+v", code, share)
	}
	if status, body := getRaw(t, server.URL+"/v1/shares/"+share.Token); status != http.StatusOK || !strings.Contains(string(body), "mcp__apps__chart") || strings.Contains(string(body), "ui_resource") {
		t.Fatalf("public share %d: %.400s", status, body)
	}

	// Deleting the session removes its stored results.
	dir := filepath.Join(root, "events", "mcp-results", sess.ID)
	if _, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodDelete, server.URL+"/v1/sessions/"+sess.ID, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("delete status %d", resp.StatusCode)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("results dir after delete: %v", err)
	}
}

func jsonNumber(n int) string {
	data, _ := json.Marshal(n)
	return string(data)
}

// A result the tool could not keep (over the limit) has no result_url.
func TestMCPToolUIResourceWithoutStoredResult(t *testing.T) {
	dir := t.TempDir()
	ui := mcpToolUIResource(dir, "s1", "c1", tools.Result{MCPApp: &tools.MCPApp{ServerID: "apps", Tool: "big", URI: "ui://board/view.html"}})
	if ui == nil || ui.Result != nil || ui.ResultURL != "" || ui.URL == "" {
		t.Fatalf("ui = %+v", ui)
	}
	if ui := mcpToolUIResource("", "s1", "c1", tools.Result{MCPApp: &tools.MCPApp{ServerID: "apps", URI: "ui://board/view.html", Result: json.RawMessage(`{}`)}}); ui == nil || ui.ResultURL != "" || string(ui.Result) != "{}" {
		t.Fatalf("ui without event dir = %+v", ui)
	}
	for _, r := range []tools.Result{{}, {MCPApp: &tools.MCPApp{ServerID: "apps", URI: "ui://x"}, Failed: true}, {MCPApp: &tools.MCPApp{ServerID: "apps", URI: "https://x"}}} {
		if ui := mcpToolUIResource(dir, "s1", "c1", r); ui != nil {
			t.Fatalf("ui for %+v = %+v", r, ui)
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("stored without a result: %v", entries)
	}
}

// A subagent's MCP App tool result announces its view on the child event and
// in the transcript built from it.
func TestSubagentMCPAppToolResultKeepsView(t *testing.T) {
	rt := newChildTestRuntime(t)
	ev := rt.trajectoryChildEventsForRun("sub-1", "run-1")
	ev.OnToolStart("call-1", "mcp__apps__chart", `{}`)
	ev.OnToolResult("call-1", "mcp__apps__chart", tools.Result{Text: "chart ready", MCPApp: &tools.MCPApp{ServerID: "apps", Tool: "chart", URI: "ui://board/view.html", Result: json.RawMessage(`{"structuredContent":{"a":1}}`)}})
	ev.OnToolStart("call-2", "mcp__apps__chart", `{}`)
	ev.OnToolResult("call-2", "mcp__apps__chart", tools.Result{Text: "Error: x", Failed: true, MCPApp: &tools.MCPApp{ServerID: "apps", Tool: "chart", URI: "ui://board/view.html"}})
	deadline := time.Now().Add(2 * time.Second)
	var events []protocol.Event
	for {
		rt.mu.Lock()
		events = append([]protocol.Event(nil), rt.events...)
		rt.mu.Unlock()
		if len(events) == 4 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("events = %d", len(events))
		}
		time.Sleep(5 * time.Millisecond)
	}
	var end struct {
		UIResource *protocol.ToolUIResource `json:"ui_resource"`
	}
	if err := json.Unmarshal(events[1].Payload, &end); err != nil || end.UIResource == nil || end.UIResource.ToolName != "chart" ||
		end.UIResource.URL != "/v1/sessions/child-runtime/mcp-ui?server=apps&uri=ui%3A%2F%2Fboard%2Fview.html" ||
		end.UIResource.ResultURL != "/v1/sessions/child-runtime/mcp-ui/results/"+mcpResultRef("call-1") || string(end.UIResource.Result) != `{"structuredContent":{"a":1}}` {
		t.Fatalf("child tool end = %s", events[1].Payload)
	}
	if strings.Contains(string(events[3].Payload), "ui_resource") {
		t.Fatalf("failed call announced a view: %s", events[3].Payload)
	}
	if _, err := os.Stat(filepath.Join(rt.eventDir, "mcp-results", "child-runtime", mcpResultRef("call-1")+".json")); err != nil {
		t.Fatal(err)
	}
	messages := childTranscript(events, "sub-1", "", protocol.ModelRef{})
	var views []*protocol.ToolUIResource
	for _, msg := range messages {
		for _, call := range msg.ToolCalls {
			views = append(views, call.UIResource)
		}
	}
	if len(views) != 2 || views[0] == nil || views[0].ResultURL != end.UIResource.ResultURL || views[0].Result != nil || views[1] != nil {
		t.Fatalf("transcript views = %+v", views)
	}
}

// POST /v1/mcp/tools/call returns the raw result, honors _meta.ui.visibility
// for the caller, and asks the client to approve tools under an "ask" policy.
func TestMCPToolCallContract(t *testing.T) {
	server, root := appsFixtureFor(t, mcp.LiveServer{ID: "apps", Name: "Apps Board", Policy: map[string]string{"mcp__apps__summary": "ask", "mcp__apps__secret": "deny"}})
	call := func(body map[string]any) (int, mcpCallResponse) {
		t.Helper()
		body["cwd"] = root
		var out mcpCallResponse
		resp := postJSON(t, http.DefaultClient, server.URL+"/v1/mcp/tools/call", body, &out)
		resp.Body.Close()
		return resp.StatusCode, out
	}
	status, out := call(map[string]any{"name": "mcp__apps__chart", "arguments": map[string]any{}})
	if status != http.StatusOK || out.Status != "completed" || out.Failed || out.Output != "chart ready" || !strings.Contains(string(out.Result), `"structuredContent":{"series":[1,2,3],"title":"Sales"}`) {
		t.Fatalf("chart: %d %+v %s", status, out, out.Result)
	}
	// Visibility: refresh is for views only, summary for the model only.
	if _, out := call(map[string]any{"name": "mcp__apps__refresh"}); !out.Failed || out.Status != "failed" || !strings.Contains(out.Output, "unknown tool") {
		t.Fatalf("model called app-only tool: %+v", out)
	}
	if _, out := call(map[string]any{"name": "mcp__apps__refresh", "caller": "app"}); out.Failed || out.Output != "refreshed" {
		t.Fatalf("app refresh: %+v", out)
	}
	if _, out := call(map[string]any{"name": "mcp__apps__summary", "caller": "app"}); !out.Failed || !strings.Contains(out.Output, "unknown tool") {
		t.Fatalf("app called model-only tool: %+v", out)
	}
	if status, _ := call(map[string]any{"name": "mcp__apps__chart", "caller": "robot"}); status != http.StatusBadRequest {
		t.Fatalf("bad caller status %d", status)
	}
	if _, out := call(map[string]any{"name": "mcp__apps__secret"}); !out.Failed || out.Status != "failed" || out.Approval != nil || out.Result != nil {
		t.Fatalf("denied: %+v", out)
	}

	// Ask: the first call does not run and returns an approval id.
	_, asked := call(map[string]any{"name": "mcp__apps__summary", "arguments": map[string]any{"a": 1}})
	if asked.Status != "approval_required" || !asked.Failed || asked.Cancelled || asked.Result != nil || asked.Approval == nil ||
		!strings.HasPrefix(asked.Approval.ID, "mcpa_") || asked.Approval.ServerID != "apps" || asked.Approval.ToolName != "summary" || asked.Approval.Policy != "ask" ||
		asked.Approval.Name != "mcp__apps__summary" || time.Until(asked.Approval.ExpiresAt) <= 0 {
		t.Fatalf("ask: %+v", asked)
	}
	// The approval is bound to the arguments, and is spent by a mismatch.
	if status, _ := call(map[string]any{"name": "mcp__apps__summary", "arguments": map[string]any{"a": 2}, "approval_id": asked.Approval.ID}); status != http.StatusConflict {
		t.Fatalf("mismatched approval status %d", status)
	}
	if status, _ := call(map[string]any{"name": "mcp__apps__summary", "arguments": map[string]any{"a": 1}, "approval_id": asked.Approval.ID}); status != http.StatusConflict {
		t.Fatalf("spent approval status %d", status)
	}
	_, asked = call(map[string]any{"name": "mcp__apps__summary", "arguments": map[string]any{"a": 1}})
	status, out = call(map[string]any{"name": "mcp__apps__summary", "arguments": json.RawMessage(`{ "a" : 1 }`), "approval_id": asked.Approval.ID})
	if status != http.StatusOK || out.Status != "completed" || out.Failed || out.Output != "summary" || out.Result == nil {
		t.Fatalf("approved: %d %+v", status, out)
	}
	if status, _ := call(map[string]any{"name": "mcp__apps__summary", "arguments": map[string]any{"a": 1}, "approval_id": asked.Approval.ID}); status != http.StatusConflict {
		t.Fatalf("reused approval status %d", status)
	}

	// Listing per audience, with the server's configured id and display name.
	list := func(query string) (int, map[string]map[string]any) {
		t.Helper()
		status, body := getRaw(t, server.URL+"/v1/mcp/tools?"+query)
		var listing struct {
			Tools []map[string]any `json:"tools"`
		}
		_ = json.Unmarshal(body, &listing)
		out := map[string]map[string]any{}
		for _, tool := range listing.Tools {
			out[tool["name"].(string)] = tool
		}
		return status, out
	}
	_, model := list("")
	_, app := list("audience=app")
	if _, ok := model["mcp__apps__refresh"]; ok {
		t.Fatalf("model listing has app-only tool")
	}
	if _, ok := app["mcp__apps__summary"]; ok {
		t.Fatalf("app listing has model-only tool")
	}
	refresh := app["mcp__apps__refresh"]
	if refresh == nil || refresh["serverId"] != "apps" || refresh["serverKey"] != "apps" || refresh["serverLabel"] != "Apps Board" || refresh["serverToolName"] != "refresh" ||
		refresh["toolName"] != "refresh" || refresh["uiResourceUri"] != "ui://board/view.html" || len(refresh["visibility"].([]any)) != 1 {
		t.Fatalf("refresh view = %v", refresh)
	}
	if vis := model["mcp__apps__board"]["visibility"].([]any); len(vis) != 2 {
		t.Fatalf("board visibility = %v", vis)
	}
	if status, _ := list("audience=robot"); status != http.StatusBadRequest {
		t.Fatalf("bad audience status %d", status)
	}
}

// serverId is the configured id even when the model-facing name uses a key.
func TestMCPToolListingReportsConfiguredServerID(t *testing.T) {
	server, _ := appsFixtureFor(t, mcp.LiveServer{ID: "My Apps"})
	status, body := getRaw(t, server.URL+"/v1/mcp/tools")
	var listing struct {
		Tools []map[string]any `json:"tools"`
	}
	if status != http.StatusOK || json.Unmarshal(body, &listing) != nil || len(listing.Tools) == 0 {
		t.Fatalf("listing %d: %s", status, body)
	}
	key, _, _ := mcp.ParseToolName(mcp.ToolName("My Apps", "board"))
	if key == "My Apps" {
		t.Fatal("fixture id should need a key")
	}
	for _, tool := range listing.Tools {
		if tool["serverId"] != "My Apps" || tool["serverKey"] != key || tool["serverLabel"] != "My Apps" || !strings.HasPrefix(tool["name"].(string), "mcp__"+key+"__") {
			t.Fatalf("tool view = %v", tool)
		}
	}
}
