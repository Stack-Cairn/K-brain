package backend

import (
	"errors"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/resources"
)

// trustInput is the POST /v1/trust body. Trusted defaults to true; false
// revokes like DELETE.
type trustInput struct {
	Workdir string `json:"workdir"`
	Trusted *bool  `json:"trusted,omitempty"`
}

// trustStatus is the reply of every /v1/trust method. Workdir is the canonical
// folder the decision applies to; Source is the recorded folder (Workdir or an
// ancestor) that decided it, empty when nothing covers Workdir.
type trustStatus struct {
	Version string `json:"version"`
	Workdir string `json:"workdir"`
	Trusted bool   `json:"trusted"`
	Source  string `json:"source,omitempty"`
}

// handleTrust serves project folder trust (capabilities.folder_trust):
//
//	GET    /v1/trust?workdir=<abs>  status
//	POST   /v1/trust {workdir, trusted?}  trust (or revoke with trusted:false)
//	DELETE /v1/trust?workdir=<abs>  revoke
//
// Trust is read from trusted_folders.toml on every use, so project prompts and
// slash commands follow a change on their next request.
func (s *Server) handleTrust(w http.ResponseWriter, r *http.Request) {
	var workdir string
	grant := false
	switch r.Method {
	case http.MethodGet, http.MethodDelete:
		workdir = r.URL.Query().Get("workdir")
	case http.MethodPost:
		var in trustInput
		if err := decodeJSON(w, r, &in); err != nil {
			return
		}
		workdir = in.Workdir
		grant = in.Trusted == nil || *in.Trusted
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	canonical, err := trustWorkdir(workdir)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	switch {
	case r.Method == http.MethodPost && grant:
		err = config.Trust(canonical)
	case r.Method == http.MethodPost || r.Method == http.MethodDelete:
		err = config.Untrust(canonical)
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	trusted, source := config.TrustDecision(canonical)
	writeJSON(w, http.StatusOK, trustStatus{Version: protocol.Version, Workdir: canonical, Trusted: trusted, Source: source})
}

// trustWorkdir requires an absolute path to an existing directory and returns
// it canonicalized the same way prompt and slash command lookups do.
func trustWorkdir(workdir string) (string, error) {
	workdir = strings.TrimSpace(workdir)
	if workdir == "" {
		return "", errors.New("workdir is required")
	}
	if !filepath.IsAbs(workdir) {
		return "", errors.New("workdir must be absolute")
	}
	return resources.CanonicalWorkdir(workdir)
}
