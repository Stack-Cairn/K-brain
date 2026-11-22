package backend

import (
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/mcp"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// toolUIResource is the MCP App view to announce with a tool.result: only for
// successful MCP tool calls whose tool declares a ui:// resource.
func (s *Server) toolUIResource(sessionID, cwd, name string, result tools.Result) *protocol.ToolUIResource {
	if s.mcp == nil || result.Failed || result.Cancelled || !strings.HasPrefix(name, "mcp__") {
		return nil
	}
	ui, ok := s.mcp.ToolUI(cwd, name)
	if !ok {
		return nil
	}
	query := url.Values{"server": {ui.ServerID}, "uri": {ui.URI}}
	return &protocol.ToolUIResource{ServerID: ui.ServerID, URI: ui.URI, URL: "/v1/sessions/" + url.PathEscape(sessionID) + "/mcp-ui?" + query.Encode()}
}

// sandboxPolicy is the CSP for generated or server-provided HTML rendered in
// a sandboxed frame: inline scripts and styles, data:/blob: media, and only
// the listed https origins for network access and external resources.
func sandboxPolicy(connect, resource []string) string {
	external := ""
	if len(resource) > 0 {
		external = " " + strings.Join(resource, " ")
	}
	connectSrc := "'none'"
	if len(connect) > 0 {
		connectSrc = strings.Join(connect, " ")
	}
	return "default-src 'none'; script-src 'unsafe-inline'" + external + "; style-src 'unsafe-inline'" + external +
		"; img-src data: blob:" + external + "; font-src data:" + external + "; media-src data: blob:" + external +
		"; connect-src " + connectSrc + "; frame-src 'none'; worker-src 'none'; form-action 'none'; base-uri 'none'; object-src 'none'"
}

var headTag = regexp.MustCompile(`(?i)<head(\s[^>]*)?>`)
var doctypeTag = regexp.MustCompile(`(?i)^\s*<!doctype[^>]*>`)

// withPolicyMeta puts policy in a <meta http-equiv> at the start of the
// document, so the restrictions also hold when a client renders the HTML
// through srcdoc, where response headers do not apply. (The sandbox itself
// cannot be set by <meta>; the frame's sandbox attribute provides it.)
func withPolicyMeta(html, policy string) string {
	meta := `<meta http-equiv="Content-Security-Policy" content="` + strings.ReplaceAll(policy, `"`, "&quot;") + `">`
	if loc := headTag.FindStringIndex(html); loc != nil {
		return html[:loc[1]] + meta + html[loc[1]:]
	}
	if loc := doctypeTag.FindStringIndex(html); loc != nil {
		return html[:loc[1]] + meta + html[loc[1]:]
	}
	return meta + html
}

// writeSandboxedHTML serves HTML for a sandboxed frame: the CSP header with
// sandbox allow-scripts (never allow-same-origin) and the same policy in a
// <meta> for srcdoc rendering.
func writeSandboxedHTML(w http.ResponseWriter, r *http.Request, html, policy string) {
	setSandboxHeaders(w, "text/html; charset=utf-8", "sandbox allow-scripts; "+policy)
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write([]byte(withPolicyMeta(html, policy)))
	}
}

// serveMCPUI serves GET /v1/sessions/{id}/mcp-ui?server=&uri=: the HTML of an
// MCP App view, read from the MCP server with resources/read for the
// session's working directory. Only a ui:// resource that a tool of an
// enabled server allowed in that directory declares, and whose policy does
// not deny it, is read.
func (s *Server) serveMCPUI(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if s.mcp == nil {
		writeJSONError(w, http.StatusNotImplemented, "MCP is unavailable")
		return
	}
	serverID, uri := r.URL.Query().Get("server"), r.URL.Query().Get("uri")
	if serverID == "" || !strings.HasPrefix(uri, mcp.UIResourceScheme) {
		writeJSONError(w, http.StatusBadRequest, "server and a ui:// uri are required")
		return
	}
	meta, _, err := s.store.Load(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "session not found")
		return
	}
	resource, err := s.mcp.ReadUIResource(r.Context(), meta.CWD, serverID, uri)
	switch {
	case errors.Is(err, mcp.ErrUIResourceNotFound):
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	case errors.Is(err, mcp.ErrUIResourceInvalid):
		writeJSONError(w, http.StatusBadGateway, err.Error())
		return
	case err != nil:
		writeJSONError(w, http.StatusBadGateway, "MCP resources/read failed: "+err.Error())
		return
	}
	writeSandboxedHTML(w, r, resource.HTML, sandboxPolicy(resource.ConnectDomains, resource.ResourceDomains))
}
