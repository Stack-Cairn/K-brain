package backend

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/mcp"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// mcpApprovalTTL is how long a client has to approve a call that needs it.
const mcpApprovalTTL = 5 * time.Minute

// maxMCPApprovals bounds the pending approvals; the oldest go first.
const maxMCPApprovals = 256

// mcpApprovalRequired is the approval a call needs before it runs.
type mcpApprovalRequired struct {
	// ID approves the call when sent back as approval_id with the same name,
	// arguments and cwd.
	ID string `json:"id"`
	// Name is the model-facing tool name.
	Name string `json:"name"`
	// ServerID is the configured MCP server; ToolName the server's own name.
	ServerID  string    `json:"server_id,omitempty"`
	ToolName  string    `json:"tool_name,omitempty"`
	Policy    string    `json:"policy"`
	Arguments any       `json:"arguments"`
	ExpiresAt time.Time `json:"expires_at"`
}

type mcpApproval struct {
	key     string
	expires time.Time
}

// mcpApprovals holds the calls of POST /v1/mcp/tools/call that wait for the
// client to approve them: an "ask" policy cannot prompt from inside an HTTP
// request, so the backend answers approval_required and runs the call when
// the client repeats it with the approval id.
type mcpApprovals struct {
	mu      sync.Mutex
	pending map[string]mcpApproval
}

func (a *mcpApprovals) issue(key string, now time.Time) (string, time.Time) {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	id := "mcpa_" + hex.EncodeToString(buf)
	expires := now.Add(mcpApprovalTTL)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pending == nil {
		a.pending = map[string]mcpApproval{}
	}
	a.pruneLocked(now)
	for len(a.pending) >= maxMCPApprovals {
		oldest := ""
		for k, v := range a.pending {
			if oldest == "" || v.expires.Before(a.pending[oldest].expires) {
				oldest = k
			}
		}
		delete(a.pending, oldest)
	}
	a.pending[id] = mcpApproval{key: key, expires: expires}
	return id, expires
}

// redeem spends approval id for the call key; false when id is unknown,
// expired, or approves another call (it is spent either way).
func (a *mcpApprovals) redeem(id, key string, now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	approval, ok := a.pending[id]
	if !ok {
		return false
	}
	delete(a.pending, id)
	return approval.key == key && now.Before(approval.expires)
}

func (a *mcpApprovals) pruneLocked(now time.Time) {
	for k, v := range a.pending {
		if !now.Before(v.expires) {
			delete(a.pending, k)
		}
	}
}

// mcpCallKey binds an approval to one call: caller, tool, cwd and arguments
// (compacted, so whitespace does not matter).
func mcpCallKey(request mcpCallRequest, audience string) string {
	args := bytes.TrimSpace(request.Arguments)
	if len(args) == 0 || bytes.Equal(args, []byte("null")) {
		args = []byte("{}")
	}
	var compact bytes.Buffer
	if json.Compact(&compact, args) == nil {
		args = compact.Bytes()
	}
	return strings.Join([]string{audience, request.Name, request.CWD, string(args)}, "\x00")
}

// callMCPTool runs POST /v1/mcp/tools/call. A tool whose server policy is
// "ask" does not run until the client approves it: the first request answers
// status "approval_required" with an approval id, and the same request with
// approval_id set runs it.
func (s *Server) callMCPTool(w http.ResponseWriter, r *http.Request, request mcpCallRequest) {
	audience, ok := mcpAudience(request.Caller)
	if !ok {
		writeJSONError(w, http.StatusBadRequest, `caller must be "model" or "app"`)
		return
	}
	key := mcpCallKey(request, audience)
	approved := false
	if request.ApprovalID != "" {
		if !s.mcpApprovals.redeem(request.ApprovalID, key, time.Now()) {
			writeJSONError(w, http.StatusConflict, "approval_id is unknown, expired, already used, or approves another call")
			return
		}
		approved = true
	}
	asked := false
	ctx := tools.WithGate(r.Context(), func(tools.GateRequest) (tools.GateDecision, string) {
		if approved {
			return tools.GateAllowOnce, ""
		}
		asked = true
		return tools.GateReject, "approval required"
	})
	toolList := s.mcp.ToolsForAudience(ctx, request.CWD, nil, audience)
	result := tools.ExecuteResult(ctx, toolList, request.Name, request.Arguments, false)
	out := mcpCallResponse{Name: request.Name, Output: result.Text, Failed: result.Failed, Cancelled: result.Cancelled, Status: "completed"}
	switch {
	case asked && !approved:
		id, expires := s.mcpApprovals.issue(key, time.Now())
		approval := &mcpApprovalRequired{ID: id, Name: request.Name, Policy: "ask", Arguments: json.RawMessage("{}"), ExpiresAt: expires}
		if len(bytes.TrimSpace(request.Arguments)) > 0 && json.Valid(request.Arguments) {
			approval.Arguments = request.Arguments
		}
		if info, ok := s.mcp.ToolInfos([]string{request.Name})[request.Name]; ok {
			approval.ServerID, approval.ToolName = info.ServerID, info.Tool
		}
		out.Status, out.Failed, out.Cancelled, out.Approval = "approval_required", true, false, approval
		out.Output = "MCP tool " + request.Name + " needs the user's approval: repeat the call with approval_id"
	case result.Cancelled:
		out.Status = "cancelled"
	case result.Failed:
		out.Status = "failed"
	}
	if !asked || approved {
		if app := result.MCPApp; app != nil && len(app.Result) > 0 && len(app.Result) <= mcp.MaxToolResultBytes {
			out.Result = app.Result
		}
	}
	writeJSON(w, http.StatusOK, out)
}
