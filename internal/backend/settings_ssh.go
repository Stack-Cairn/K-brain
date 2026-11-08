package backend

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/elicit"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/sshhosts"
	"github.com/Stack-Cairn/K-brain/internal/sshmgr"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// SSH settings:
//
//	GET    /v1/settings/ssh                          hosts (secrets blanked), associations, known_hosts path
//	PUT    /v1/settings/ssh                          replace hosts and/or associations
//	PUT    /v1/settings/ssh/hosts/{id}               add or update one host
//	DELETE /v1/settings/ssh/hosts/{id}               delete one host
//	DELETE /v1/settings/ssh/hosts/{id}/host-keys     forget the host's trusted keys
//
// Secrets (password, privateKey, privateKeyPassphrase, proxy.password) are
// write-only: responses report them only through the "...Configured" flags.
const sshSettingsPath = "/v1/settings/ssh"

func (s *Server) handleSSHSettings(w http.ResponseWriter, r *http.Request) {
	if s.ssh == nil {
		writeJSONError(w, http.StatusNotImplemented, "SSH settings are unavailable")
		return
	}
	store := s.ssh.Hosts()
	if r.URL.Path == sshSettingsPath {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, store.Public())
		case http.MethodPut:
			var in sshhosts.DocumentInput
			if err := decodeJSON(w, r, &in); err != nil {
				return
			}
			_, _, err := store.Update(in.BaseRevision, in.ApplyTo)
			s.finishSSHUpdate(w, err)
		default:
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}
	id, keys, ok := sshHostPath(r.URL.EscapedPath())
	if !ok {
		writeJSONError(w, http.StatusNotFound, "not found")
		return
	}
	switch {
	case keys && r.Method == http.MethodDelete:
		host, found := store.Host(id)
		if !found {
			writeJSONError(w, http.StatusNotFound, sshhosts.ErrNotFound.Error())
			return
		}
		removed, err := store.KnownHosts().Remove(host.Address())
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "could not update known_hosts: "+err.Error())
			return
		}
		// A session that trusted the old key keeps running until it closes;
		// close it so the next connection checks the key again.
		s.ssh.CloseHostSessions(id)
		writeJSON(w, http.StatusOK, map[string]any{"version": protocol.Version, "removed": removed, "host": store.KnownHosts().Public(host)})
	case keys:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	case r.Method == http.MethodPut:
		var in sshhosts.HostInput
		if err := decodeJSON(w, r, &in); err != nil {
			return
		}
		if strings.TrimSpace(in.ID) == "" {
			in.ID = id
		}
		if strings.TrimSpace(in.ID) != id {
			writeJSONError(w, http.StatusBadRequest, "host id in the body does not match the path")
			return
		}
		_, _, err := store.Update(0, func(doc *sshhosts.Document) error {
			_, err := sshhosts.PutHost(doc, in)
			return err
		})
		s.finishSSHUpdate(w, err)
	case r.Method == http.MethodDelete:
		_, _, err := store.Update(0, func(doc *sshhosts.Document) error { return sshhosts.DeleteHost(doc, id) })
		s.finishSSHUpdate(w, err)
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// finishSSHUpdate answers a settings change: the new public document, or the
// error. Sessions whose host changed or was deleted are closed.
func (s *Server) finishSSHUpdate(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sshhosts.ErrConflict):
		writeJSONError(w, http.StatusConflict, err.Error())
		return
	case errors.Is(err, sshhosts.ErrNotFound):
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	case errors.Is(err, sshhosts.ErrSave):
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	case err != nil:
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.ssh.Sync()
	writeJSON(w, http.StatusOK, s.ssh.Hosts().Public())
}

// sshHostPath parses /v1/settings/ssh/hosts/{id}[/host-keys].
func sshHostPath(escaped string) (id string, keys bool, ok bool) {
	rest, found := strings.CutPrefix(escaped, sshSettingsPath+"/hosts/")
	if !found {
		return "", false, false
	}
	if before, cut := strings.CutSuffix(rest, "/host-keys"); cut {
		rest, keys = before, true
	}
	if rest == "" || strings.Contains(rest, "/") {
		return "", false, false
	}
	id, err := url.PathUnescape(rest)
	if err != nil || id == "" || strings.ContainsAny(id, "/\\?#") {
		return "", false, false
	}
	return id, keys, true
}

// sshManagerTool is SSHManager for a run in workingDir, or nil when no SSH
// host is associated with that project (the tool is then not offered, as in
// LiveAgent v1). Associations made later take effect on the next run.
func (s *Server) sshManagerTool(workingDir string, options protocol.RunOptions) *tools.Tool {
	if s.ssh == nil || !s.ssh.Hosts().HasProjectHosts(workingDir) {
		return nil
	}
	tool := sshmgr.Tool(s.ssh, sshmgr.ToolOptions{Interaction: func(ctx context.Context) sshmgr.Interaction {
		inter := sshmgr.Interaction{Unattended: isUnattendedRun(ctx), Ask: tools.AskHandler(ctx)}
		// Login forms need a client that shows elicitation forms.
		if options.MCPElicitation {
			inter.Elicit = elicit.Handler(ctx)
		}
		return inter
	}})
	return &tool
}

// withSSHManagerTool adds SSHManager to a run's extra tools unless the
// client declared its own tool of that name.
func (s *Server) withSSHManagerTool(extra []tools.Tool, workingDir string, options protocol.RunOptions) []tools.Tool {
	for _, t := range extra {
		if t.Def.Function.Name == sshmgr.ToolName {
			return extra
		}
	}
	if tool := s.sshManagerTool(workingDir, options); tool != nil {
		return append(extra, *tool)
	}
	return extra
}
