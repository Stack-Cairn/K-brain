package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCP Apps: a tool may declare, in its _meta, an HTML resource (a ui://
// URI) that a graphical client renders in a sandboxed frame next to the
// tool's result. K-brain only surfaces the declaration and reads the
// resource for the client; it never renders or runs it.

const (
	// UIResourceScheme prefixes every UI resource URI.
	UIResourceScheme = "ui://"
	// MaxUIResourceBytes bounds the HTML read for one UI resource.
	MaxUIResourceBytes = 4 << 20
	uiReadTimeout      = 30 * time.Second
)

// uiResourceURI returns the UI resource a tool's _meta declares:
// _meta.ui.resourceUri, or the flat "ui/resourceUri" key of earlier drafts.
// Anything that is not a ui:// URI is ignored.
func uiResourceURI(meta sdkmcp.Meta) string {
	var uri string
	if ui, ok := meta["ui"].(map[string]any); ok {
		uri, _ = ui["resourceUri"].(string)
	}
	if uri == "" {
		uri, _ = meta["ui/resourceUri"].(string)
	}
	uri = strings.TrimSpace(uri)
	if !strings.HasPrefix(uri, UIResourceScheme) || len(uri) == len(UIResourceScheme) || len(uri) > 2048 {
		return ""
	}
	return uri
}

// ToolUI is the UI resource a model-facing MCP tool declares.
type ToolUI struct {
	// ServerID is the configured server (LiveServer.ID).
	ServerID string
	// URI is the ui:// resource.
	URI string
}

// UIResource is a UI resource's HTML as read from its server.
type UIResource struct {
	URI      string
	MIMEType string
	HTML     string
	// ConnectDomains and ResourceDomains are the https origins the resource's
	// _meta.ui.csp asks to reach (fetch/WebSocket) and to load scripts,
	// styles, images, fonts and media from. Entries that are not plain https
	// origins are dropped.
	ConnectDomains  []string
	ResourceDomains []string
}

var (
	// ErrUIResourceNotFound: no usable server or tool declares the resource.
	ErrUIResourceNotFound = errors.New("MCP UI resource not found")
	// ErrUIResourceInvalid: the server's reply is not a usable HTML resource.
	ErrUIResourceInvalid = errors.New("MCP UI resource is not usable HTML")
)

// toolUIs maps the model-facing names of serverName's tools to the UI
// resource each declares; nil while the server is not connected.
func (m *Manager) toolUIs(serverName string) map[string]string {
	m.onChangeMu.Lock()
	s := m.servers[serverName]
	m.onChangeMu.Unlock()
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defs, sess := s.defs, s.sess
	s.mu.Unlock()
	if sess == nil {
		return nil
	}
	out := map[string]string{}
	for _, d := range defs {
		if uri := uiResourceURI(d.Meta); uri != "" {
			out[ToolName(serverName, d.Name)] = uri
		}
	}
	return out
}

// readResource reads uri from serverName.
func (m *Manager) readResource(ctx context.Context, serverName, uri string) (*sdkmcp.ReadResourceResult, error) {
	m.onChangeMu.Lock()
	s := m.servers[serverName]
	m.onChangeMu.Unlock()
	if s == nil {
		return nil, ErrUIResourceNotFound
	}
	s.mu.Lock()
	sess := s.sess
	s.mu.Unlock()
	if sess == nil {
		return nil, fmt.Errorf("MCP server %q is not connected", serverName)
	}
	ctx, cancel := context.WithTimeout(ctx, uiReadTimeout)
	defer cancel()
	return sess.ReadResource(ctx, &sdkmcp.ReadResourceParams{URI: uri})
}

// uiServer returns the configured server id usable from cwd: enabled and
// allowed by its workspaceRoots.
func (m *LiveManager) uiServer(cwd, serverID string) (LiveServer, *Manager, bool) {
	m.mu.RLock()
	settings, manager := cloneLiveSettings(m.settings), m.manager
	m.mu.RUnlock()
	if manager == nil {
		return LiveServer{}, nil, false
	}
	for _, server := range settings.Servers {
		if server.ID == serverID && server.Enabled && workspaceAllowed(cwd, server.WorkspaceRoots) {
			return server, manager, true
		}
	}
	return LiveServer{}, nil, false
}

func toolDenied(server LiveServer, toolName string) bool {
	policy := strings.ToLower(server.Policy[toolName])
	if policy == "" {
		policy = strings.ToLower(server.Policy["*"])
	}
	return policy == "deny"
}

// ToolUI returns the UI resource the model-facing MCP tool toolName
// declares, when its server is enabled and allowed from cwd and its policy
// does not deny the tool.
func (m *LiveManager) ToolUI(cwd, toolName string) (ToolUI, bool) {
	key, _, ok := ParseToolName(toolName)
	if !ok {
		return ToolUI{}, false
	}
	m.mu.RLock()
	var ids []string
	for _, server := range m.settings.Servers {
		if serverKeyFor(server.ID) == key {
			ids = append(ids, server.ID)
		}
	}
	m.mu.RUnlock()
	for _, id := range ids {
		server, manager, ok := m.uiServer(cwd, id)
		if !ok || toolDenied(server, toolName) {
			continue
		}
		if uri := manager.toolUIs(id)[toolName]; uri != "" {
			return ToolUI{ServerID: id, URI: uri}, true
		}
	}
	return ToolUI{}, false
}

// ToolUIs maps the model-facing names of the tools in list that declare a UI
// resource (see ToolUI) to that resource.
func (m *LiveManager) ToolUIs(cwd string, names []string) map[string]ToolUI {
	out := map[string]ToolUI{}
	for _, name := range names {
		if ui, ok := m.ToolUI(cwd, name); ok {
			out[name] = ui
		}
	}
	return out
}

// ReadUIResource reads the UI resource uri from server serverID for a session
// in cwd. Only a resource that one of the server's tools declares, and whose
// policy does not deny that tool, is read: the route cannot be used to read
// arbitrary resources.
func (m *LiveManager) ReadUIResource(ctx context.Context, cwd, serverID, uri string) (UIResource, error) {
	if !strings.HasPrefix(uri, UIResourceScheme) {
		return UIResource{}, ErrUIResourceNotFound
	}
	server, manager, ok := m.uiServer(cwd, serverID)
	if !ok {
		return UIResource{}, ErrUIResourceNotFound
	}
	declared := false
	for toolName, declaredURI := range manager.toolUIs(serverID) {
		if declaredURI == uri && !toolDenied(server, toolName) {
			declared = true
			break
		}
	}
	if !declared {
		return UIResource{}, ErrUIResourceNotFound
	}
	result, err := manager.readResource(ctx, serverID, uri)
	if err != nil {
		return UIResource{}, err
	}
	return pickUIContent(result, uri)
}

// pickUIContent takes the HTML content for uri from a resources/read reply.
func pickUIContent(result *sdkmcp.ReadResourceResult, uri string) (UIResource, error) {
	if result == nil {
		return UIResource{}, ErrUIResourceInvalid
	}
	for _, content := range result.Contents {
		if content == nil || (content.URI != "" && content.URI != uri) {
			continue
		}
		mime := strings.ToLower(strings.TrimSpace(content.MIMEType))
		if !strings.HasPrefix(mime, "text/html") {
			continue
		}
		html := content.Text
		if html == "" && len(content.Blob) > 0 {
			html = string(content.Blob)
		}
		if html == "" {
			continue
		}
		if len(html) > MaxUIResourceBytes {
			return UIResource{}, fmt.Errorf("%w: larger than %d bytes", ErrUIResourceInvalid, MaxUIResourceBytes)
		}
		out := UIResource{URI: uri, MIMEType: content.MIMEType, HTML: html}
		out.ConnectDomains, out.ResourceDomains = uiCSPDomains(content.Meta)
		return out, nil
	}
	return UIResource{}, ErrUIResourceInvalid
}

// uiCSPDomains reads _meta.ui.csp {connectDomains, resourceDomains}, keeping
// only plain https origins.
func uiCSPDomains(meta sdkmcp.Meta) (connect, resource []string) {
	ui, _ := meta["ui"].(map[string]any)
	csp, _ := ui["csp"].(map[string]any)
	return httpsOrigins(csp["connectDomains"]), httpsOrigins(csp["resourceDomains"])
}

func httpsOrigins(v any) []string {
	list, _ := v.([]any)
	var out []string
	seen := map[string]bool{}
	for _, item := range list {
		raw, _ := item.(string)
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
			continue
		}
		valid := true
		for _, c := range u.Host {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == ':') {
				valid = false
			}
		}
		origin := "https://" + strings.ToLower(u.Host)
		if valid && !seen[origin] && len(out) < 16 {
			seen[origin] = true
			out = append(out, origin)
		}
	}
	return out
}
