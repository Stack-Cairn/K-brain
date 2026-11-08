package backend

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/Stack-Cairn/K-brain/internal/skills"
)

func (s *Server) handleSkills(w http.ResponseWriter, r *http.Request) {
	m, err := skills.NewManager()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if r.URL.Path == "/v1/skills" && r.Method == http.MethodGet {
		workdir := r.URL.Query().Get("workdir")
		rev, state, err := m.Settings(workdir)
		if err != nil {
			writeJSONError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		installed, shadowed, invalid, err := m.ListAll()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		// skills holds one entry per name; shadowed pack copies are listed apart
		// so clients that key skills by name never see duplicates.
		writeJSON(w, http.StatusOK, map[string]any{"version": "v1", "revision": rev, "rootDir": m.Root(), "skills": installed, "shadowed": shadowed, "invalid": invalid, "enabled": state.Enabled, "selected": state.Selected, "settings": state, "warnings": []string{}})
		return
	}
	if r.URL.Path == "/v1/skills/files" && r.Method == http.MethodGet {
		installed, _, err := m.List()
		if err != nil {
			writeJSONError(w, 500, err.Error())
			return
		}
		paths := []string{}
		for _, v := range installed {
			paths = append(paths, v.SkillFile)
		}
		writeJSON(w, 200, map[string]any{"rootDir": m.Root(), "paths": paths, "truncated": false})
		return
	}
	if r.URL.Path == "/v1/skills/file" && r.Method == http.MethodGet {
		content, truncated, n, err := m.Read(r.URL.Query().Get("path"), queryInt(r, "offset", 0), queryInt(r, "length", 200))
		if err != nil {
			writeJSONError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"content": content, "truncated": truncated, "startLine": queryInt(r, "offset", 0) + 1, "numLines": n})
		return
	}
	if r.URL.Path == "/v1/skills/settings" && r.Method == http.MethodPut {
		var in struct {
			Workdir    string   `json:"workdir"`
			Enabled    bool     `json:"enabled"`
			Selected   []string `json:"selected"`
			Mode       string   `json:"mode"`
			SkillNames []string `json:"skillNames"`
			// Packs is omitted by clients without skill packs; the stored value
			// is kept then. {} clears it.
			Packs map[string]bool `json:"packs"`
		}
		if err := decodeJSON(w, r, &in); err != nil {
			return
		}
		rev, err := m.SaveSettings(in.Workdir, skills.SkillSettings{Enabled: in.Enabled, Selected: in.Selected, Mode: in.Mode, SkillNames: in.SkillNames, Packs: in.Packs})
		if err != nil {
			writeJSONError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		_, set, _ := m.Settings(in.Workdir)
		writeJSON(w, 200, map[string]any{"version": "v1", "revision": rev, "settings": set})
		return
	}
	if r.URL.Path == "/v1/skills/store/search" && r.Method == http.MethodGet {
		args := map[string]any{"action": "clawhub_search", "query": r.URL.Query().Get("q"), "cursor": r.URL.Query().Get("cursor"), "ownerHandle": r.URL.Query().Get("ownerHandle")}
		out, e := m.Manage(r.Context(), args)
		if e != nil {
			writeJSONError(w, http.StatusBadGateway, "ClawHub search failed: "+e.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"version": "v1", "results": out.ClawhubResults, "nextCursor": out.ClawhubNextCursor})
		return
	}
	if r.URL.Path == "/v1/skills/store/install" && r.Method == http.MethodPost {
		var in map[string]any
		if err := decodeJSON(w, r, &in); err != nil {
			return
		}
		in["action"] = "clawhub_install"
		out, e := m.Manage(r.Context(), in)
		if e != nil {
			writeJSONError(w, http.StatusBadGateway, "ClawHub install failed: "+e.Error())
			return
		}
		writeJSON(w, 200, out)
		return
	}
	if r.URL.Path != "/v1/skills/manage" || r.Method != http.MethodPost {
		writeJSONError(w, http.StatusNotFound, "skills route not found")
		return
	}
	var in map[string]any
	if err := decodeJSON(w, r, &in); err != nil {
		return
	}
	out, err := m.Manage(r.Context(), in)
	if err != nil {
		status := http.StatusUnprocessableEntity
		if errors.Is(err, context.Canceled) {
			status = http.StatusRequestTimeout
		}
		writeJSONError(w, status, err.Error())
		return
	}
	writeJSON(w, 200, out)
}
func queryInt(r *http.Request, k string, d int) int {
	v := r.URL.Query().Get(k)
	if v == "" {
		return d
	}
	var n int
	if _, e := fmt.Sscanf(v, "%d", &n); e != nil || n < 0 {
		return d
	}
	return n
}
