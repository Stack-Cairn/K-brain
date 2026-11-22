package backend

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/mcp"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

type mcpToolsRequest struct {
	CWD       string   `json:"cwd,omitempty"`
	ServerIDs []string `json:"server_ids,omitempty"`
}

type mcpSearchRequest struct {
	Query     string   `json:"query"`
	CWD       string   `json:"cwd,omitempty"`
	ServerIDs []string `json:"server_ids,omitempty"`
	MaxResult int      `json:"max_results,omitempty"`
}

type mcpCallRequest struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	CWD       string          `json:"cwd,omitempty"`
	// Caller is who asks for the call: "model" (default) or "app" for an MCP
	// App view; each may call only the tools _meta.ui.visibility allows it.
	Caller string `json:"caller,omitempty"`
	// ApprovalID approves a call an earlier response marked
	// approval_required; it is single-use and bound to name, arguments and cwd.
	ApprovalID string `json:"approval_id,omitempty"`
}

// mcpCallResponse is the reply of POST /v1/mcp/tools/call.
type mcpCallResponse struct {
	Name      string `json:"name"`
	Output    string `json:"output"`
	Failed    bool   `json:"failed"`
	Cancelled bool   `json:"cancelled"`
	// Status is "completed", "failed", "cancelled" or "approval_required".
	Status string `json:"status"`
	// Result is the server's raw CallToolResult (content, structuredContent,
	// _meta, isError), when the call reached the server and the result is
	// within mcp.MaxToolResultBytes.
	Result   json.RawMessage      `json:"result,omitempty"`
	Approval *mcpApprovalRequired `json:"approval,omitempty"`
}

func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	if s.mcp == nil {
		writeJSONError(w, http.StatusNotImplemented, "MCP is unavailable")
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/mcp":
		writeJSON(w, http.StatusOK, map[string]any{"settings": redactMCPSettings(s.mcp.Settings()), "statuses": s.mcp.Statuses()})
	case r.Method == http.MethodPut && r.URL.Path == "/v1/mcp":
		var settings mcp.LiveSettings
		if decodeJSON(w, r, &settings) != nil {
			return
		}
		if err := s.mcp.Replace(r.Context(), settings); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"settings": redactMCPSettings(s.mcp.Settings()), "statuses": s.mcp.Statuses()})
	case r.Method == http.MethodPost && r.URL.Path == "/v1/mcp/reload":
		if err := s.mcp.Reload(); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"statuses": s.mcp.Statuses()})
	case r.Method == http.MethodGet && r.URL.Path == "/v1/mcp/tools":
		request := mcpToolsRequest{CWD: r.URL.Query().Get("cwd"), ServerIDs: splitIDs(r.URL.Query().Get("server_ids"))}
		audience, ok := mcpAudience(r.URL.Query().Get("audience"))
		if !ok {
			writeJSONError(w, http.StatusBadRequest, `audience must be "model" or "app"`)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"tools": s.mcpToolViews(request.CWD, s.mcp.ToolsForAudience(r.Context(), request.CWD, request.ServerIDs, audience))})
	case r.Method == http.MethodPost && r.URL.Path == "/v1/mcp/search":
		var request mcpSearchRequest
		if decodeJSON(w, r, &request) != nil {
			return
		}
		if strings.TrimSpace(request.Query) == "" {
			writeJSONError(w, http.StatusBadRequest, "query is required")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"tools": s.mcpToolViews(request.CWD, s.mcp.Search(r.Context(), request.Query, request.CWD, request.ServerIDs, request.MaxResult))})
	case r.Method == http.MethodPost && r.URL.Path == "/v1/mcp/tools/call":
		var request mcpCallRequest
		if decodeJSON(w, r, &request) != nil {
			return
		}
		if request.Name == "" {
			writeJSONError(w, http.StatusBadRequest, "name is required")
			return
		}
		s.callMCPTool(w, r, request)
	default:
		writeJSONError(w, http.StatusNotFound, "MCP route not found")
	}
}

// mcpAudience parses the audience of a tool listing or call: "model" (the
// default) or "app".
func mcpAudience(v string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", mcp.AudienceModel:
		return mcp.AudienceModel, true
	case mcp.AudienceApp:
		return mcp.AudienceApp, true
	}
	return "", false
}

// mcpToolViews lists tools for the MCP routes, with uiResourceUri on tools
// that declare an MCP App view.
func (s *Server) mcpToolViews(cwd string, list []tools.Tool) []map[string]any {
	names := make([]string, 0, len(list))
	for _, tool := range list {
		names = append(names, tool.Def.Function.Name)
	}
	uis := s.mcp.ToolUIs(cwd, names)
	infos := s.mcp.ToolInfos(names)
	labels := s.mcp.ServerNames()
	out := toolViews(list)
	for _, view := range out {
		name := view["name"].(string)
		if ui, ok := uis[name]; ok {
			view["uiResourceUri"] = ui.URI
		}
		info, ok := infos[name]
		if !ok {
			continue
		}
		// serverId is the configured id; it was the name key before, which
		// differs for ids that are not simple names, so serverKey keeps it.
		view["serverId"] = info.ServerID
		view["serverKey"] = info.ServerKey
		view["serverLabel"] = info.ServerID
		if label, ok := labels[info.ServerID]; ok {
			view["serverLabel"] = label
		}
		view["serverToolName"] = info.Tool
		view["visibility"] = info.Visibility
	}
	return out
}

func toolViews(list []tools.Tool) []map[string]any {
	out := make([]map[string]any, 0, len(list))
	for _, tool := range list {
		var schema any
		_ = json.Unmarshal([]byte(tool.Def.Function.Parameters), &schema)
		serverID, rawName, _ := mcp.ParseToolName(tool.Def.Function.Name)
		out = append(out, map[string]any{"name": tool.Def.Function.Name, "serverId": serverID, "serverKey": serverID, "serverLabel": serverID, "toolName": rawName, "description": tool.Def.Function.Description, "inputSchema": schema})
	}
	return out
}

func redactMCPSettings(settings mcp.LiveSettings) mcp.LiveSettings {
	out := settings
	out.Servers = make([]mcp.LiveServer, len(settings.Servers))
	for i, server := range settings.Servers {
		out.Servers[i] = server
		out.Servers[i].Env = nil
		out.Servers[i].Headers = nil
		if server.Auth != nil {
			auth := *server.Auth
			auth.CredentialRef = ""
			out.Servers[i].Auth = &auth
		}
	}
	return out
}

func splitIDs(raw string) []string {
	if raw == "" {
		return nil
	}
	out := make([]string, 0)
	for _, item := range strings.Split(raw, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}
