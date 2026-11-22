package mcp

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/mcp/mcptest"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func appsLiveManager(t *testing.T, server LiveServer) *LiveManager {
	t.Helper()
	m := NewManager(map[string]ServerConfig{server.ID: testCfg(server.ID)})
	m.connectTransport = func(context.Context, ServerConfig, *ringBuffer) (sdkmcp.Transport, error) {
		clientT, serverT := sdkmcp.NewInMemoryTransports()
		ss, err := mcptest.NewAppsServer().Connect(context.Background(), serverT, nil)
		if err != nil {
			return nil, err
		}
		t.Cleanup(func() { ss.Close() })
		return clientT, nil
	}
	t.Cleanup(m.Close)
	m.Start(context.Background())
	deadline := time.Now().Add(5 * time.Second)
	for len(m.Tools()) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("apps server not ready: %+v", m.Statuses())
		}
		time.Sleep(10 * time.Millisecond)
	}
	return &LiveManager{settings: LiveSettings{Servers: []LiveServer{server}}, manager: m}
}

func TestToolUIReadsMetaAndHonorsPolicy(t *testing.T) {
	live := appsLiveManager(t, LiveServer{ID: "apps", Enabled: true, Transport: "stdio", Policy: map[string]string{"mcp__apps__secret": "deny"}})
	cwd := t.TempDir()
	if ui, ok := live.ToolUI(cwd, "mcp__apps__board"); !ok || ui != (ToolUI{ServerID: "apps", URI: "ui://board/view.html"}) {
		t.Fatalf("board ui = %+v %v", ui, ok)
	}
	if ui, ok := live.ToolUI(cwd, "mcp__apps__legacy"); !ok || ui.URI != "ui://legacy/view.html" {
		t.Fatalf("legacy ui = %+v %v", ui, ok)
	}
	for _, name := range []string{"mcp__apps__plain", "mcp__apps__secret", "mcp__other__board", "Read"} {
		if ui, ok := live.ToolUI(cwd, name); ok {
			t.Errorf("%s ui = %+v", name, ui)
		}
	}
	uis := live.ToolUIs(cwd, []string{"mcp__apps__board", "mcp__apps__plain"})
	if len(uis) != 1 || uis["mcp__apps__board"].URI != "ui://board/view.html" {
		t.Fatalf("ToolUIs = %+v", uis)
	}

	res, err := live.ReadUIResource(t.Context(), cwd, "apps", "ui://board/view.html")
	if err != nil || res.HTML != mcptest.AppsHTML || res.MIMEType != "text/html;profile=mcp-app" {
		t.Fatalf("board resource = %+v %v", res, err)
	}
	if !slices.Equal(res.ConnectDomains, []string{"https://api.example.com"}) || !slices.Equal(res.ResourceDomains, []string{"https://cdn.example.com"}) {
		t.Fatalf("csp domains = %v %v", res.ConnectDomains, res.ResourceDomains)
	}
	if res, err := live.ReadUIResource(t.Context(), cwd, "apps", "ui://legacy/view.html"); err != nil || res.HTML != "<p>legacy</p>" {
		t.Fatalf("blob resource = %+v %v", res, err)
	}
	for uri, want := range map[string]error{
		"ui://secret/view.html":     ErrUIResourceNotFound, // its only tool is denied
		"ui://undeclared/view.html": ErrUIResourceNotFound, // no tool declares it
		"file:///etc/passwd":        ErrUIResourceNotFound,
		"ui://notes/view.txt":       ErrUIResourceInvalid, // not HTML
	} {
		if _, err := live.ReadUIResource(t.Context(), cwd, "apps", uri); !errors.Is(err, want) {
			t.Errorf("%s: err = %v, want %v", uri, err, want)
		}
	}
	if _, err := live.ReadUIResource(t.Context(), cwd, "missing", "ui://board/view.html"); !errors.Is(err, ErrUIResourceNotFound) {
		t.Errorf("unknown server: %v", err)
	}
}

func TestToolUIRespectsWorkspaceRootsAndEnabled(t *testing.T) {
	root := t.TempDir()
	live := appsLiveManager(t, LiveServer{ID: "apps", Enabled: true, Transport: "stdio", WorkspaceRoots: []string{root}})
	if _, ok := live.ToolUI(t.TempDir(), "mcp__apps__board"); ok {
		t.Fatal("UI offered outside the server's workspace roots")
	}
	if _, err := live.ReadUIResource(t.Context(), t.TempDir(), "apps", "ui://board/view.html"); !errors.Is(err, ErrUIResourceNotFound) {
		t.Fatalf("read outside workspace roots: %v", err)
	}
	if _, ok := live.ToolUI(root, "mcp__apps__board"); !ok {
		t.Fatal("UI missing inside the workspace root")
	}
	live.settings.Servers[0].Enabled = false
	if _, ok := live.ToolUI(root, "mcp__apps__board"); ok {
		t.Fatal("UI offered for a disabled server")
	}
}

func TestUIResourceURIAcceptsOnlyUIScheme(t *testing.T) {
	for meta, want := range map[string]string{
		`ui`:     "ui://a/b.html",
		`flat`:   "ui://flat.html",
		`https`:  "",
		`empty`:  "",
		`scheme`: "",
	} {
		m := map[string]sdkmcp.Meta{
			"ui":     {"ui": map[string]any{"resourceUri": "ui://a/b.html"}},
			"flat":   {"ui/resourceUri": "ui://flat.html"},
			"https":  {"ui": map[string]any{"resourceUri": "https://example.com/x.html"}},
			"empty":  {},
			"scheme": {"ui": map[string]any{"resourceUri": "ui://"}},
		}[meta]
		if got := uiResourceURI(m); got != want {
			t.Errorf("%s: %q, want %q", meta, got, want)
		}
	}
}
