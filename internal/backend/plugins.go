package backend

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/hooks"
	"github.com/Stack-Cairn/K-brain/internal/plugins"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/resources"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// PluginHost loads K-brain plugins (internal/plugins) for the backend the way
// the CLI, terminal UI and ACP modes do: the user's plugins for every session,
// and a workspace's own plugins (<workdir>/.liveagent/plugins) only while that
// folder is trusted (config.TrustDecision). Trust is read on every use, so
// granting or revoking it through /v1/trust takes effect on the next request
// or turn.
type PluginHost struct {
	newManager func(project string) (*plugins.Manager, error)

	mu       sync.Mutex
	managers map[string]*plugins.Manager // keyed by trusted canonical workdir; "" holds user plugins only
	loadErrs map[string]string
	prints   map[string][32]byte
	revision uint64
	changed  chan struct{}
}

// NewPluginHost returns a host that loads plugins from the standard
// directories (plugins.Dirs).
func NewPluginHost() *PluginHost {
	return &PluginHost{newManager: plugins.New, managers: map[string]*plugins.Manager{}, loadErrs: map[string]string{}, prints: map[string][32]byte{}, changed: make(chan struct{})}
}

// pluginProject is the project whose workspace plugins a session in workdir
// may load: the canonical workdir when it is trusted, "" otherwise.
func pluginProject(workdir string) string {
	workdir = strings.TrimSpace(workdir)
	if workdir == "" || !filepath.IsAbs(workdir) {
		return ""
	}
	canonical, err := resources.CanonicalWorkdir(workdir)
	if err != nil || !config.Trusted(canonical) {
		return ""
	}
	return canonical
}

func (h *PluginHost) manager(workdir string) (*plugins.Manager, string, error) {
	project := pluginProject(workdir)
	h.mu.Lock()
	defer h.mu.Unlock()
	m, err := h.managerLocked(project)
	return m, project, err
}

func (h *PluginHost) managerLocked(project string) (*plugins.Manager, error) {
	if m := h.managers[project]; m != nil {
		return m, nil
	}
	m, err := h.newManager(project)
	if m == nil {
		if err == nil {
			err = errors.New("plugins unavailable")
		}
		return nil, err
	}
	h.managers[project] = m
	h.loadErrs[project] = errText(err)
	return m, nil
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// changedLocked rescans every cached manager so they agree with the state one
// of them saved, and wakes revision waiters.
func (h *PluginHost) changedLocked() {
	for project, m := range h.managers {
		h.loadErrs[project] = errText(m.Reload())
	}
	h.bumpLocked()
}

func (h *PluginHost) bumpLocked() {
	h.revision++
	close(h.changed)
	h.changed = make(chan struct{})
}

// pluginView is one entry of GET /v1/plugins.
type pluginView struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Version     string            `json:"version,omitempty"`
	Description string            `json:"description,omitempty"`
	Source      string            `json:"source"`
	Enabled     bool              `json:"enabled"`
	Path        string            `json:"path"`
	Contributes pluginContributes `json:"contributes"`
}

// pluginContributes lists what a plugin adds to sessions. Tools are the
// model-facing names (plugin_<plugin>_<tool>); hooks are the lifecycle events
// it runs commands for; prompt says whether it adds system prompt text.
type pluginContributes struct {
	Tools  []string `json:"tools"`
	Hooks  []string `json:"hooks"`
	Prompt bool     `json:"prompt"`
}

type pluginList struct {
	Version  string       `json:"version"`
	Workdir  string       `json:"workdir,omitempty"`
	Trusted  bool         `json:"trusted"`
	Revision uint64       `json:"revision"`
	Plugins  []pluginView `json:"plugins"`
	Errors   []string     `json:"errors,omitempty"`
}

func viewPlugin(m *plugins.Manager, p plugins.Plugin) pluginView {
	view := pluginView{ID: p.Name, Name: p.Name, Version: p.Version, Description: p.Description, Source: m.Source(p), Enabled: p.Enabled, Path: p.Dir,
		Contributes: pluginContributes{Tools: []string{}, Hooks: []string{}, Prompt: strings.TrimSpace(p.Prompt) != ""}}
	for _, tool := range p.Tools {
		view.Contributes.Tools = append(view.Contributes.Tools, "plugin_"+p.Name+"_"+tool.Name)
	}
	for event, command := range p.Hooks {
		if strings.TrimSpace(command) != "" {
			view.Contributes.Hooks = append(view.Contributes.Hooks, event)
		}
	}
	sort.Strings(view.Contributes.Hooks)
	return view
}

// list rescans the plugins visible from workdir. A listing that differs from
// the previous one for the same scope (a change made by the kn CLI, the
// terminal UI or by hand) advances the revision too.
func (h *PluginHost) list(workdir string) (pluginList, error) {
	project := pluginProject(workdir)
	h.mu.Lock()
	defer h.mu.Unlock()
	m, err := h.managerLocked(project)
	if err != nil {
		return pluginList{}, err
	}
	h.loadErrs[project] = errText(m.Reload())
	out := pluginList{Version: protocol.Version, Trusted: project != "", Plugins: []pluginView{}}
	if canonical, canonicalErr := resources.CanonicalWorkdir(strings.TrimSpace(workdir)); canonicalErr == nil {
		out.Workdir = canonical
	}
	for _, p := range m.List() {
		out.Plugins = append(out.Plugins, viewPlugin(m, p))
	}
	if msg := h.loadErrs[project]; msg != "" {
		out.Errors = strings.Split(msg, "; ")
	}
	data, _ := json.Marshal(out)
	print := sha256.Sum256(data)
	if previous, seen := h.prints[project]; seen && previous != print {
		h.bumpLocked()
	}
	h.prints[project] = print
	out.Revision = h.revision
	return out, nil
}

// waitChange blocks until the revision passes since, wait elapses or ctx ends.
func (h *PluginHost) waitChange(ctx context.Context, since uint64, wait time.Duration) {
	h.mu.Lock()
	revision, changed := h.revision, h.changed
	h.mu.Unlock()
	if revision > since || wait <= 0 {
		return
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-changed:
	case <-timer.C:
	case <-ctx.Done():
	}
}

var errWorkspacePluginRemove = errors.New("workspace plugins belong to the project; remove them from its .liveagent/plugins directory")

func (h *PluginHost) mutate(workdir string, change func(*plugins.Manager) error) error {
	project := pluginProject(workdir)
	h.mu.Lock()
	defer h.mu.Unlock()
	m, err := h.managerLocked(project)
	if err != nil {
		return err
	}
	// Rescan first: the plugin may have been added or removed by another process.
	h.loadErrs[project] = errText(m.Reload())
	if err := change(m); err != nil {
		return err
	}
	h.changedLocked()
	return nil
}

func (h *PluginHost) setEnabled(workdir, id string, enabled bool) error {
	return h.mutate(workdir, func(m *plugins.Manager) error { return m.SetEnabled(id, enabled) })
}

func (h *PluginHost) remove(workdir, id string) error {
	return h.mutate(workdir, func(m *plugins.Manager) error {
		p, ok := m.Get(id)
		if !ok {
			return errPluginNotFound
		}
		if m.Source(p) == "workspace" {
			return errWorkspacePluginRemove
		}
		return m.Remove(id)
	})
}

func (h *PluginHost) install(workdir, src string) error {
	return h.mutate(workdir, func(m *plugins.Manager) error { return m.Install(src) })
}

func (h *PluginHost) reload(workdir string) error {
	return h.mutate(workdir, func(*plugins.Manager) error { return nil })
}

var errPluginNotFound = errors.New("plugin not found")

// Tools rescans and returns the enabled plugins' tools for a session in
// workdir. Each call asks for permission like other tools that run commands,
// and resolves the plugin again when it runs, so a plugin disabled (or a
// folder untrusted) since the turn started no longer runs.
func (h *PluginHost) Tools(workdir string) []tools.Tool {
	m, err := h.refresh(workdir)
	if err != nil {
		return nil
	}
	var out []tools.Tool
	for _, spec := range m.Tools() {
		name := spec.Name
		schema := spec.InputSchema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		out = append(out, tools.Tool{Def: ai.NewTool(name, spec.Description, string(schema)), Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			if err := tools.Authorize(ctx, name, string(args)); err != nil {
				return "", err
			}
			current, _, err := h.manager(workdir)
			if err != nil {
				return "", err
			}
			return current.Invoke(ctx, name, args, nil)
		}})
	}
	return out
}

// refresh rescans the plugins visible from workdir, picking up changes made
// by other processes; called once per turn.
func (h *PluginHost) refresh(workdir string) (*plugins.Manager, error) {
	project := pluginProject(workdir)
	h.mu.Lock()
	defer h.mu.Unlock()
	m, err := h.managerLocked(project)
	if err != nil {
		return nil, err
	}
	h.loadErrs[project] = errText(m.Reload())
	return m, nil
}

// PromptBlock is the enabled plugins' system prompt text for workdir.
func (h *PluginHost) PromptBlock(workdir string) string {
	m, _, err := h.manager(workdir)
	if err != nil {
		return ""
	}
	return m.PromptBlock()
}

// RunHook runs the enabled plugins' commands for a lifecycle event.
func (h *PluginHost) RunHook(ctx context.Context, workdir string, event hooks.Event) error {
	m, _, err := h.manager(workdir)
	if err != nil {
		return nil
	}
	return m.RunHook(ctx, event)
}

// attachPlugins wires plugin hooks, tools and prompt text into a new agent.
func (s *Server) attachPlugins(ag *agent.Agent) {
	if s.plugins == nil {
		return
	}
	cwd := ag.WorkingDir
	host := s.plugins
	ag.PluginHook = func(ctx context.Context, event hooks.Event) error { return host.RunHook(ctx, cwd, event) }
	ag.SetPluginTools(host.Tools(cwd))
	ag.SetPromptAddendum(func() string { return host.PromptBlock(cwd) })
}

// syncPluginTools refreshes an agent's plugin tools before a turn.
func (s *Server) syncPluginTools(ag *agent.Agent) {
	if s.plugins == nil || ag == nil {
		return
	}
	ag.SetPluginTools(s.plugins.Tools(ag.WorkingDir))
}

type pluginRequest struct {
	Workdir string `json:"workdir,omitempty"`
	Path    string `json:"path,omitempty"`
}

// handlePlugins serves capabilities.plugins:
//
//	GET    /v1/plugins?workdir=&since=&wait=  list (long-polls with since/wait)
//	POST   /v1/plugins/reload {workdir?}       rescan the plugin directories
//	POST   /v1/plugins/install {path, workdir?} copy a plugin directory into the user's plugins
//	POST   /v1/plugins/{id}/enable {workdir?}
//	POST   /v1/plugins/{id}/disable {workdir?}
//	DELETE /v1/plugins/{id}?workdir=           remove a user plugin
//
// Every reply except errors is the listing for workdir.
func (s *Server) handlePlugins(w http.ResponseWriter, r *http.Request) {
	if s.plugins == nil {
		writeJSONError(w, http.StatusNotImplemented, "plugins are unavailable")
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/plugins"), "/")
	parts := strings.Split(rest, "/")
	if rest == "" {
		parts = nil
	}
	var in pluginRequest
	if r.Method == http.MethodPost {
		if decodeOptionalJSON(w, r, &in) != nil {
			return
		}
	}
	workdir := in.Workdir
	if workdir == "" {
		workdir = r.URL.Query().Get("workdir")
	}
	if workdir != "" && !filepath.IsAbs(strings.TrimSpace(workdir)) {
		writeJSONError(w, http.StatusBadRequest, "workdir must be absolute")
		return
	}
	var err error
	switch {
	case len(parts) == 0 && r.Method == http.MethodGet:
		since, sinceErr := intQuery(r, "since", -1, 0, 1<<53-1)
		wait, waitErr := intQuery(r, "wait", 0, 0, 60)
		if sinceErr != nil || waitErr != nil {
			writeJSONError(w, http.StatusBadRequest, errors.Join(sinceErr, waitErr).Error())
			return
		}
		if since >= 0 {
			s.plugins.waitChange(r.Context(), uint64(since), time.Duration(wait)*time.Second)
		}
	case len(parts) == 1 && parts[0] == "reload" && r.Method == http.MethodPost:
		err = s.plugins.reload(workdir)
	case len(parts) == 1 && parts[0] == "install" && r.Method == http.MethodPost:
		path := strings.TrimSpace(in.Path)
		if path == "" || !filepath.IsAbs(path) {
			writeJSONError(w, http.StatusBadRequest, "path must be an absolute plugin directory")
			return
		}
		err = s.plugins.install(workdir, path)
	case len(parts) == 2 && (parts[1] == "enable" || parts[1] == "disable") && r.Method == http.MethodPost:
		err = s.plugins.setEnabled(workdir, parts[0], parts[1] == "enable")
	case len(parts) == 1 && r.Method == http.MethodDelete:
		err = s.plugins.remove(workdir, parts[0])
	default:
		writeJSONError(w, http.StatusNotFound, "plugin route not found")
		return
	}
	if err != nil {
		status := http.StatusBadRequest
		switch {
		case errors.Is(err, errPluginNotFound) || strings.Contains(err.Error(), "not found"):
			status = http.StatusNotFound
		case errors.Is(err, errWorkspacePluginRemove) || strings.Contains(err.Error(), "already installed"):
			status = http.StatusConflict
		}
		writeJSONError(w, status, err.Error())
		return
	}
	list, err := s.plugins.list(workdir)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// decodeOptionalJSON decodes a JSON body when there is one.
func decodeOptionalJSON(w http.ResponseWriter, r *http.Request, v any) error {
	if r.Body == nil || r.ContentLength == 0 {
		return nil
	}
	return decodeJSON(w, r, v)
}
