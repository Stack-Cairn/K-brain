package mcptest

import (
	"context"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// AppsServerEnv, when set in a test binary's environment, tells its TestMain
// to serve NewAppsServer over stdio instead of running tests.
const AppsServerEnv = "K_BRAIN_TEST_FAKE_MCP_APPS"

// AppsHTML is the HTML of ui://board/view.html.
const AppsHTML = "<!doctype html><html><head><title>Board</title></head><body><div id=app>board</div><script>window.parent.postMessage({jsonrpc:'2.0',id:1,method:'ui/initialize'},'*')</script></body></html>"

// NewAppsServer returns an MCP server whose tools declare UI resources:
//
//   - board: _meta.ui.resourceUri ui://board/view.html (text/html;profile=mcp-app,
//     with _meta.ui.csp domains, some of them invalid)
//   - legacy: the flat _meta "ui/resourceUri" key, ui://legacy/view.html (a blob)
//   - secret: ui://secret/view.html, for tests that deny the tool by policy
//   - notes: ui://notes/view.txt, a resource that is not HTML
//   - plain: no UI
//
// ui://undeclared/view.html exists but no tool declares it.
func NewAppsServer() *sdkmcp.Server {
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "fake-apps", Version: "1.0.0"}, nil)
	object := map[string]any{"type": "object"}
	text := func(s string) sdkmcp.ToolHandler {
		return func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: s}}}, nil
		}
	}
	ui := func(uri string) sdkmcp.Meta { return sdkmcp.Meta{"ui": map[string]any{"resourceUri": uri}} }
	server.AddTool(&sdkmcp.Tool{Name: "board", Description: "Show the board.", InputSchema: object, Meta: ui("ui://board/view.html")}, text("board ready"))
	server.AddTool(&sdkmcp.Tool{Name: "legacy", Description: "Legacy UI key.", InputSchema: object, Meta: sdkmcp.Meta{"ui/resourceUri": "ui://legacy/view.html"}}, text("legacy ready"))
	server.AddTool(&sdkmcp.Tool{Name: "secret", Description: "Denied in tests.", InputSchema: object, Meta: ui("ui://secret/view.html")}, text("secret"))
	server.AddTool(&sdkmcp.Tool{Name: "notes", Description: "Non-HTML UI.", InputSchema: object, Meta: ui("ui://notes/view.txt")}, text("notes"))
	server.AddTool(&sdkmcp.Tool{Name: "plain", Description: "No UI.", InputSchema: object, Meta: ui("https://example.invalid/not-ui")}, text("plain"))
	resource := func(uri, mime string, contents func() *sdkmcp.ResourceContents) {
		server.AddResource(&sdkmcp.Resource{URI: uri, Name: uri, MIMEType: mime}, func(context.Context, *sdkmcp.ReadResourceRequest) (*sdkmcp.ReadResourceResult, error) {
			return &sdkmcp.ReadResourceResult{Contents: []*sdkmcp.ResourceContents{contents()}}, nil
		})
	}
	resource("ui://board/view.html", "text/html;profile=mcp-app", func() *sdkmcp.ResourceContents {
		return &sdkmcp.ResourceContents{URI: "ui://board/view.html", MIMEType: "text/html;profile=mcp-app", Text: AppsHTML,
			Meta: sdkmcp.Meta{"ui": map[string]any{"csp": map[string]any{
				"connectDomains":  []any{"https://api.example.com", "http://insecure.example.com", "javascript:alert(1)", "https://x.example.com/path"},
				"resourceDomains": []any{"https://cdn.example.com/", "https://a b.example.com"},
			}}}}
	})
	resource("ui://legacy/view.html", "text/html", func() *sdkmcp.ResourceContents {
		return &sdkmcp.ResourceContents{URI: "ui://legacy/view.html", MIMEType: "text/html", Blob: []byte("<p>legacy</p>")}
	})
	resource("ui://secret/view.html", "text/html", func() *sdkmcp.ResourceContents {
		return &sdkmcp.ResourceContents{URI: "ui://secret/view.html", MIMEType: "text/html", Text: "<p>secret</p>"}
	})
	resource("ui://notes/view.txt", "text/plain", func() *sdkmcp.ResourceContents {
		return &sdkmcp.ResourceContents{URI: "ui://notes/view.txt", MIMEType: "text/plain", Text: "notes"}
	})
	resource("ui://undeclared/view.html", "text/html", func() *sdkmcp.ResourceContents {
		return &sdkmcp.ResourceContents{URI: "ui://undeclared/view.html", MIMEType: "text/html", Text: "<p>undeclared</p>"}
	})
	return server
}

// ServeApps serves NewAppsServer on stdin/stdout until the client leaves.
func ServeApps(ctx context.Context) error {
	return NewAppsServer().Run(ctx, &sdkmcp.StdioTransport{})
}
