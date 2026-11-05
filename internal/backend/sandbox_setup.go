package backend

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/sandbox"
)

// POST /v1/sandbox/setup installs (or repairs) the elevated level of the
// native Windows sandbox for the user the backend runs as. It shows the
// administrator approval prompt on the user's desktop and waits for it; a
// declined prompt is a 403 with code "declined". The response carries the
// setup status and the new run_sandbox capability, which a client may also
// re-read from /v1/health (capabilityRevision changes).

var (
	// sandboxSetup is replaced in tests.
	sandboxSetup = sandbox.Setup
	// sandboxSetupMu allows one setup at a time.
	sandboxSetupMu sync.Mutex
	// sandboxSetupChanges counts setups that ran in this process.
	sandboxSetupChanges atomic.Uint64
)

type sandboxSetupResponse struct {
	Version    string              `json:"version"`
	Status     sandbox.SetupStatus `json:"status"`
	RunSandbox healthRunSandboxV1  `json:"run_sandbox"`
	Error      string              `json:"error,omitempty"`
	Code       string              `json:"code,omitempty"`
}

func (s *Server) handleSandboxSetup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSONError(w, http.StatusMethodNotAllowed, "use POST")
		return
	}
	if !sandboxSetupMu.TryLock() {
		writeJSON(w, http.StatusConflict, sandboxSetupResponse{Version: protocol.Version, Error: "a sandbox setup is already running", Code: "busy"})
		return
	}
	defer sandboxSetupMu.Unlock()
	// The approval prompt waits for the user; a client that gives up does not
	// cancel a setup that was approved.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Minute)
	defer cancel()
	status, err := sandboxSetup(ctx)
	if !errors.Is(err, sandbox.ErrSetupUnsupported) {
		sandboxSetupChanges.Add(1)
	}
	resp := sandboxSetupResponse{Version: protocol.Version, Status: status, RunSandbox: runSandboxCapability()}
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, resp)
	case errors.Is(err, sandbox.ErrSetupUnsupported):
		resp.Error, resp.Code = err.Error(), "unsupported"
		writeJSON(w, http.StatusNotImplemented, resp)
	case errors.Is(err, sandbox.ErrSetupDeclined):
		resp.Error, resp.Code = err.Error(), "declined"
		writeJSON(w, http.StatusForbidden, resp)
	default:
		resp.Error, resp.Code = err.Error(), "failed"
		writeJSON(w, http.StatusInternalServerError, resp)
	}
}
