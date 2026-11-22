package mcp

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func TestToolVisibilityParsesMeta(t *testing.T) {
	both := []string{AudienceModel, AudienceApp}
	for _, tc := range []struct {
		meta sdkmcp.Meta
		want []string
	}{
		{nil, both},
		{sdkmcp.Meta{"ui": map[string]any{"resourceUri": "ui://x"}}, both},
		{sdkmcp.Meta{"ui": map[string]any{"visibility": []any{}}}, both},
		{sdkmcp.Meta{"ui": map[string]any{"visibility": []any{"app"}}}, []string{AudienceApp}},
		{sdkmcp.Meta{"ui": map[string]any{"visibility": []any{" Model "}}}, []string{AudienceModel}},
		{sdkmcp.Meta{"ui": map[string]any{"visibility": []any{"app", "model", 3}}}, both},
		{sdkmcp.Meta{"ui": map[string]any{"visibility": []any{"other"}}}, both},
		{sdkmcp.Meta{"ui": map[string]any{"visibility": "app"}}, both},
	} {
		if got := toolVisibility(tc.meta); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("toolVisibility(%v) = %v, want %v", tc.meta, got, tc.want)
		}
	}
}

func TestToolsListOnlyWhatEachAudienceMayCall(t *testing.T) {
	live := appsLiveManager(t, LiveServer{ID: "apps", Name: "Apps Board", Enabled: true, Transport: "stdio"})
	cwd := t.TempDir()
	names := func(list []tools.Tool) map[string]bool {
		out := map[string]bool{}
		for _, tool := range list {
			out[tool.Def.Function.Name] = true
		}
		return out
	}
	model := names(live.ToolsFor(context.Background(), cwd, nil))
	app := names(live.ToolsForAudience(context.Background(), cwd, nil, AudienceApp))
	if model["mcp__apps__refresh"] || !model["mcp__apps__summary"] || !model["mcp__apps__board"] {
		t.Fatalf("model tools = %v", model)
	}
	if !app["mcp__apps__refresh"] || app["mcp__apps__summary"] || !app["mcp__apps__board"] {
		t.Fatalf("app tools = %v", app)
	}
	if n := names(live.manager.Tools()); n["mcp__apps__refresh"] {
		t.Fatalf("Manager.Tools offers an app-only tool: %v", n)
	}
	infos := live.ToolInfos([]string{"mcp__apps__refresh", "mcp__apps__board", "mcp__apps__missing"})
	if got := infos["mcp__apps__refresh"]; got.ServerID != "apps" || got.ServerKey != "apps" || got.Tool != "refresh" || !reflect.DeepEqual(got.Visibility, []string{AudienceApp}) {
		t.Fatalf("refresh info = %+v", got)
	}
	if got := infos["mcp__apps__board"]; !reflect.DeepEqual(got.Visibility, []string{AudienceModel, AudienceApp}) {
		t.Fatalf("board info = %+v", got)
	}
	if _, ok := infos["mcp__apps__missing"]; ok || len(infos) != 2 {
		t.Fatalf("infos = %+v", infos)
	}
	if got := live.ServerNames(); !reflect.DeepEqual(got, map[string]string{"apps": "Apps Board"}) {
		t.Fatalf("server names = %v", got)
	}
}

func TestMCPToolReportsRawResultForViews(t *testing.T) {
	live := appsLiveManager(t, LiveServer{ID: "apps", Enabled: true, Transport: "stdio"})
	list := live.ToolsFor(context.Background(), t.TempDir(), nil)
	result := tools.ExecuteResult(context.Background(), list, "mcp__apps__chart", json.RawMessage(`{}`), false)
	if result.Failed || result.Text != "chart ready" || result.MCPApp == nil {
		t.Fatalf("chart = %+v", result)
	}
	app := result.MCPApp
	if app.ServerID != "apps" || app.Tool != "chart" || app.URI != "ui://board/view.html" {
		t.Fatalf("app = %+v", app)
	}
	var raw struct {
		Meta              map[string]any   `json:"_meta"`
		Content           []map[string]any `json:"content"`
		StructuredContent map[string]any   `json:"structuredContent"`
	}
	if err := json.Unmarshal(app.Result, &raw); err != nil {
		t.Fatal(err)
	}
	if raw.Meta["fake/trace"] != "t-1" || len(raw.Content) != 1 || raw.Content[0]["text"] != "chart ready" || raw.StructuredContent["title"] != "Sales" {
		t.Fatalf("raw result = %s", app.Result)
	}

	// A tool without a view still reports its result, with no URI.
	plain := tools.ExecuteResult(context.Background(), list, "mcp__apps__plain", json.RawMessage(`{}`), false)
	if plain.MCPApp == nil || plain.MCPApp.URI != "" || len(plain.MCPApp.Result) == 0 {
		t.Fatalf("plain = %+v", plain.MCPApp)
	}

	// A result over the limit is not kept; the text still is.
	big := tools.ExecuteResult(context.Background(), list, "mcp__apps__big", json.RawMessage(`{"bytes":`+jsonInt(MaxToolResultBytes)+`}`), false)
	if big.Failed || big.Text != "big ready" || big.MCPApp == nil || big.MCPApp.Result != nil {
		t.Fatalf("big = failed %v text %q app %v", big.Failed, big.Text, big.MCPApp != nil)
	}
}

func TestRawToolResultLimit(t *testing.T) {
	small := &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "hi"}}, StructuredContent: map[string]any{"a": 1}}
	if got := string(rawToolResult(small)); !strings.Contains(got, `"structuredContent":{"a":1}`) {
		t.Fatalf("raw = %s", got)
	}
	large := &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: strings.Repeat("x", MaxToolResultBytes)}}}
	if got := rawToolResult(large); got != nil {
		t.Fatalf("over-limit result kept: %d bytes", len(got))
	}
}

func jsonInt(n int) string {
	data, _ := json.Marshal(n)
	return string(data)
}
