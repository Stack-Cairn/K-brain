package backend

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/mcp"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// maxInlineToolResultBytes bounds the raw result a tool.result event carries
// inline; a larger one (up to mcp.MaxToolResultBytes) is only read from
// result_url.
const maxInlineToolResultBytes = 256 << 10

var mcpResultRefPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// toolUIResource is the MCP App view to announce with a tool.result: only for
// successful MCP tool calls whose tool declares a ui:// resource. The call's
// raw result is stored for result_url and, when small, sent inline.
func (s *Server) toolUIResource(sessionID, callID string, result tools.Result) *protocol.ToolUIResource {
	return mcpToolUIResource(s.eventDir, sessionID, callID, result)
}

// mcpToolUIResource is toolUIResource with the event directory the session's
// stored results live under.
func mcpToolUIResource(eventDir, sessionID, callID string, result tools.Result) *protocol.ToolUIResource {
	app := result.MCPApp
	if app == nil || result.Failed || result.Cancelled || app.ServerID == "" || !strings.HasPrefix(app.URI, mcp.UIResourceScheme) {
		return nil
	}
	ui := &protocol.ToolUIResource{ServerID: app.ServerID, URI: app.URI, ToolName: app.Tool, ResultStored: len(app.Result) > 0 && saveMCPResult(eventDir, sessionID, callID, *app)}
	if len(app.Result) <= maxInlineToolResultBytes {
		ui.Result = app.Result
	}
	return withUIResourceURLs(sessionID, callID, ui)
}

// withUIResourceURLs fills the backend paths of ui, a view of tool call
// callID in session sessionID.
func withUIResourceURLs(sessionID, callID string, ui *protocol.ToolUIResource) *protocol.ToolUIResource {
	if ui == nil {
		return nil
	}
	query := url.Values{"server": {ui.ServerID}, "uri": {ui.URI}}
	base := "/v1/sessions/" + url.PathEscape(sessionID) + "/mcp-ui"
	ui.URL = base + "?" + query.Encode()
	if ui.ResultStored {
		ui.ResultURL = base + "/results/" + mcpResultRef(callID)
	}
	return ui
}

// withHistoryUIResources fills the view paths of the MCP App views on the
// tool calls of messages, read from session sessionID's history.
func withHistoryUIResources(sessionID string, messages []protocol.Message) {
	for i := range messages {
		for j := range messages[i].ToolCalls {
			call := &messages[i].ToolCalls[j]
			call.UIResource = withUIResourceURLs(sessionID, call.ID, call.UIResource)
		}
	}
}

// mcpResultRef names a tool call's stored result: a hash, since tool call ids
// come from the model provider.
func mcpResultRef(callID string) string {
	sum := sha256.Sum256([]byte("mcp-result\x00" + callID))
	return hex.EncodeToString(sum[:16])
}

func (s *Server) mcpResultDir(sessionID string) string { return mcpResultDir(s.eventDir, sessionID) }

func mcpResultDir(eventDir, sessionID string) string {
	if eventDir == "" || sessionID == "" {
		return ""
	}
	return filepath.Join(eventDir, "mcp-results", sessionID)
}

// storedMCPResult is the file kept for one tool call's raw result.
type storedMCPResult struct {
	Version    string          `json:"version"`
	ToolCallID string          `json:"tool_call_id"`
	ServerID   string          `json:"server_id"`
	ToolName   string          `json:"tool_name"`
	URI        string          `json:"uri"`
	Result     json.RawMessage `json:"result"`
}

// saveMCPResult stores app's raw result for tool call callID; false when it
// could not be stored.
func saveMCPResult(eventDir, sessionID, callID string, app tools.MCPApp) bool {
	dir := mcpResultDir(eventDir, sessionID)
	if dir == "" || callID == "" || len(app.Result) == 0 || len(app.Result) > mcp.MaxToolResultBytes {
		return false
	}
	data, err := json.Marshal(storedMCPResult{Version: protocol.Version, ToolCallID: callID, ServerID: app.ServerID, ToolName: app.Tool, URI: app.URI, Result: app.Result})
	if err != nil || os.MkdirAll(dir, 0o700) != nil {
		return false
	}
	return writePreviewFile(filepath.Join(dir, mcpResultRef(callID)+".json"), data) == nil
}

// serveMCPResult serves GET /v1/sessions/{id}/mcp-ui/results/{ref}: the raw
// CallToolResult of an MCP tool call whose App view tool.result announced.
func (s *Server) serveMCPResult(w http.ResponseWriter, r *http.Request, id, ref string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if _, _, err := s.store.Load(id); err != nil {
		writeJSONError(w, http.StatusNotFound, "session not found")
		return
	}
	dir := s.mcpResultDir(id)
	if dir == "" || !mcpResultRefPattern.MatchString(ref) {
		writeJSONError(w, http.StatusNotFound, "MCP tool result not found")
		return
	}
	data, err := os.ReadFile(filepath.Join(dir, ref+".json"))
	if err != nil || len(data) > mcp.MaxToolResultBytes+64<<10 || !json.Valid(data) {
		writeJSONError(w, http.StatusNotFound, "MCP tool result not found")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
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
