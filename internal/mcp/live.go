package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// CredentialBridge resolves a native-host credential without moving the secret
// into the K-brain settings file or HTTP API.
type CredentialBridge func(context.Context, string) (string, error)

type LiveServer struct {
	ID             string            `json:"id"`
	Description    string            `json:"description,omitempty"`
	DocsURL        string            `json:"docsUrl,omitempty"`
	Enabled        bool              `json:"enabled"`
	Transport      string            `json:"transport"`
	Command        string            `json:"command,omitempty"`
	Args           []string          `json:"args,omitempty"`
	URL            string            `json:"url,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	Cwd            string            `json:"cwd,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	TimeoutMs      int               `json:"timeoutMs,omitempty"`
	WorkspaceRoots []string          `json:"workspaceRoots,omitempty"`
	Policy         map[string]string `json:"policy,omitempty"`
	Auth           *LiveAuth         `json:"auth,omitempty"`
}

type LiveAuth struct {
	Type          string `json:"type"`
	Scope         string `json:"scope,omitempty"`
	ClientID      string `json:"clientId,omitempty"`
	CredentialRef string `json:"credentialRef,omitempty"`
}

type LiveSettings struct {
	Servers  []LiveServer `json:"servers"`
	Selected []string     `json:"selected,omitempty"`
}

type LiveStatus struct {
	LiveServer
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
	Tools  int    `json:"tools"`
}

type LiveManager struct {
	path     string
	save     func(LiveSettings) error
	ctx      context.Context
	bridge   CredentialBridge
	onChange func()

	mu       sync.RWMutex
	settings LiveSettings
	manager  *Manager
	closed   bool
}

func OpenLiveManager(ctx context.Context, path string, fallback map[string]ServerConfig) (*LiveManager, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("mcp settings path is required")
	}
	settings, err := loadLiveSettings(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if errors.Is(err, os.ErrNotExist) {
		settings = settingsFromConfigs(fallback)
	}
	m := &LiveManager{path: path, ctx: ctx, settings: normalizeLiveSettings(settings)}
	if err := m.reloadLocked(); err != nil {
		return nil, err
	}
	return m, nil
}

func NewLiveManager(ctx context.Context, path string, settings LiveSettings) (*LiveManager, error) {
	m := &LiveManager{path: path, ctx: ctx, settings: normalizeLiveSettings(settings)}
	if err := m.reloadLocked(); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *LiveManager) SetCredentialBridge(bridge CredentialBridge) {
	m.mu.Lock()
	m.bridge = bridge
	m.mu.Unlock()
}

func (m *LiveManager) SetSave(fn func(LiveSettings) error) {
	m.mu.Lock()
	m.save = fn
	m.mu.Unlock()
}

func (m *LiveManager) SetOnChange(fn func()) {
	m.mu.Lock()
	m.onChange = fn
	if m.manager != nil {
		m.manager.SetOnChange(fn)
	}
	m.mu.Unlock()
}

func (m *LiveManager) Settings() LiveSettings {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneLiveSettings(m.settings)
}

func (m *LiveManager) Statuses() []LiveStatus {
	m.mu.RLock()
	settings, manager := cloneLiveSettings(m.settings), m.manager
	m.mu.RUnlock()
	byName := map[string]Server{}
	if manager != nil {
		for _, status := range manager.Statuses() {
			byName[status.Name] = status
		}
	}
	out := make([]LiveStatus, 0, len(settings.Servers))
	for _, server := range settings.Servers {
		st := byName[server.ID]
		if isManagedComputerServer(server) {
			out = append(out, LiveStatus{LiveServer: redactLiveServer(server), Status: "managed", Error: "Computer use is owned by K-brain computer_exec; configure it through computer settings"})
			continue
		}
		out = append(out, LiveStatus{LiveServer: redactLiveServer(server), Status: st.Status.String(), Error: st.Err, Tools: st.Tools})
	}
	return out
}

func (m *LiveManager) Replace(ctx context.Context, settings LiveSettings) error {
	settings = normalizeLiveSettings(settings)
	if err := validateLiveSettings(settings); err != nil {
		return err
	}
	m.mu.RLock()
	save := m.save
	previousServers := make(map[string]LiveServer, len(m.settings.Servers))
	for _, server := range m.settings.Servers {
		previousServers[server.ID] = server
	}
	m.mu.RUnlock()
	// GET redacts secrets; an omitted field on a settings round trip keeps it.
	// Explicit empty maps still clear credentials.
	for i := range settings.Servers {
		server := &settings.Servers[i]
		previous, exists := previousServers[server.ID]
		if !exists {
			continue
		}
		if server.Env == nil {
			server.Env = cloneMap(previous.Env)
		}
		if server.Headers == nil {
			server.Headers = cloneMap(previous.Headers)
		}
		if server.Auth != nil && previous.Auth != nil && server.Auth.Type == "oauth" && previous.Auth.Type == "oauth" && server.Auth.CredentialRef == "" {
			server.Auth.CredentialRef = previous.Auth.CredentialRef
		}
	}
	if save != nil {
		if err := save(settings); err != nil {
			return err
		}
	} else if err := saveLiveSettings(m.path, settings); err != nil {
		return err
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return errors.New("mcp manager is closed")
	}
	previous := m.settings
	m.settings = settings
	err := m.reloadLocked()
	if err != nil {
		m.settings = previous
		_ = m.reloadLocked()
	}
	fn := m.onChange
	m.mu.Unlock()
	if err != nil {
		return err
	}
	if fn != nil {
		fn()
	}
	return nil
}

func (m *LiveManager) Reload() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errors.New("mcp manager is closed")
	}
	settings, err := loadLiveSettings(m.path)
	if err != nil {
		return err
	}
	m.settings = normalizeLiveSettings(settings)
	return m.reloadLocked()
}

func (m *LiveManager) reloadLocked() error {
	configs := make(map[string]ServerConfig)
	for _, server := range m.settings.Servers {
		if isManagedComputerServer(server) {
			continue
		}
		cfg, err := m.transportConfig(server)
		if err != nil {
			return fmt.Errorf("mcp server %q: %w", server.ID, err)
		}
		if !server.Enabled {
			cfg.Enabled = new(bool)
		}
		configs[server.ID] = cfg
	}
	next := NewManager(configs)
	if m.onChange != nil {
		next.SetOnChange(m.onChange)
	}
	next.Start(m.ctx)
	old := m.manager
	m.manager = next
	if old != nil {
		old.Close()
	}
	return nil
}

func isManagedComputerServer(server LiveServer) bool {
	command := strings.ReplaceAll(strings.Trim(strings.TrimSpace(server.Command), "\""), "\\", "/")
	name := strings.ToLower(filepath.Base(command))
	return strings.EqualFold(strings.TrimSpace(server.ID), "cua-driver") || name == "cua-driver" || name == "cua-driver.exe"
}

func (m *LiveManager) transportConfig(server LiveServer) (ServerConfig, error) {
	transport := strings.ToLower(strings.TrimSpace(server.Transport))
	if transport == "" {
		if server.URL != "" {
			transport = "http"
		} else {
			transport = "stdio"
		}
	}
	cfg := ServerConfig{Env: cloneMap(server.Env), Cwd: server.Cwd, URL: server.URL, Headers: cloneMap(server.Headers), Source: "kbrain-live"}
	if !server.Enabled {
		cfg.Enabled = new(bool)
		if transport == "http" {
			cfg.URL = "http://disabled.invalid"
		} else {
			cfg.Command = []string{"true"}
		}
		return cfg, nil
	}
	switch transport {
	case "stdio":
		if strings.TrimSpace(server.Command) == "" {
			return ServerConfig{}, errors.New("transport=stdio requires command")
		}
		cfg.Command = append([]string{server.Command}, server.Args...)
	case "http":
		if strings.TrimSpace(server.URL) == "" {
			return ServerConfig{}, errors.New("transport=http requires url")
		}
	default:
		return ServerConfig{}, fmt.Errorf("unsupported transport %q", transport)
	}
	if server.TimeoutMs > 0 {
		cfg.StartupTimeout = max(1, server.TimeoutMs/1000)
		cfg.ToolTimeout = max(1, server.TimeoutMs/1000)
	}
	if server.Auth != nil && strings.EqualFold(server.Auth.Type, "oauth") {
		credentialRef := server.Auth.CredentialRef
		if credentialRef == "" {
			credentialRef = server.ID
		}
		if m.bridge == nil {
			return ServerConfig{}, errors.New("oauth credential bridge is unavailable")
		}
		credential, err := m.bridge(m.ctx, credentialRef)
		if err != nil {
			return ServerConfig{}, fmt.Errorf("resolve oauth credential: %w", err)
		}
		if cfg.Headers == nil {
			cfg.Headers = map[string]string{}
		}
		cfg.Headers["Authorization"] = "Bearer " + credential
	}
	return cfg, nil
}

func (m *LiveManager) ToolsFor(ctx context.Context, cwd string, selected []string) []tools.Tool {
	m.mu.RLock()
	settings, manager := cloneLiveSettings(m.settings), m.manager
	m.mu.RUnlock()
	if len(selected) == 0 && len(settings.Selected) > 0 {
		selected = settings.Selected
	}
	if manager == nil {
		return nil
	}
	allowed := map[string]LiveServer{}
	selectedSet := map[string]bool{}
	for _, id := range selected {
		selectedSet[id] = true
	}
	for _, server := range settings.Servers {
		if !server.Enabled || (len(selected) > 0 && !selectedSet[server.ID]) || !workspaceAllowed(cwd, server.WorkspaceRoots) {
			continue
		}
		allowed[server.ID] = server
	}
	all := manager.Tools()
	out := make([]tools.Tool, 0, len(all))
	for _, tool := range all {
		serverKey, _, ok := ParseToolName(tool.Def.Function.Name)
		if !ok {
			continue
		}
		var server LiveServer
		found := false
		for id, candidate := range allowed {
			if serverKey == serverKeyFor(id) {
				server, found = candidate, true
				break
			}
		}
		if !found {
			continue
		}
		tool = applyPolicy(tool, server)
		out = append(out, tool)
	}
	return out
}

func (m *LiveManager) Search(ctx context.Context, query string, cwd string, selected []string, limit int) []tools.Tool {
	if limit <= 0 || limit > 10 {
		limit = 5
	}
	terms := strings.Fields(strings.ToLower(query))
	candidates := m.ToolsFor(ctx, cwd, selected)
	type scored struct {
		tool  tools.Tool
		score int
	}
	ranked := make([]scored, 0, len(candidates))
	for _, tool := range candidates {
		text := strings.ToLower(tool.Def.Function.Name + " " + tool.Def.Function.Description)
		score := 0
		for _, term := range terms {
			if strings.Contains(text, term) {
				score++
			}
		}
		if score > 0 || len(terms) == 0 {
			ranked = append(ranked, scored{tool, score})
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].tool.Def.Function.Name < ranked[j].tool.Def.Function.Name
	})
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	out := make([]tools.Tool, len(ranked))
	for i := range ranked {
		out[i] = ranked[i].tool
	}
	return out
}

func (m *LiveManager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	manager := m.manager
	m.manager = nil
	m.mu.Unlock()
	if manager != nil {
		manager.Close()
	}
}

func applyPolicy(tool tools.Tool, server LiveServer) tools.Tool {
	policy := strings.ToLower(server.Policy[tool.Def.Function.Name])
	if policy == "" {
		policy = strings.ToLower(server.Policy["*"])
	}
	if policy == "" || policy == "allow" {
		return tool
	}
	run := tool.Run
	tool.Run = func(ctx context.Context, args json.RawMessage) (string, error) {
		if policy == "deny" {
			return "", fmt.Errorf("MCP tool %q is denied by policy", tool.Def.Function.Name)
		}
		if policy != "ask" {
			return "", fmt.Errorf("unsupported MCP tool policy %q", policy)
		}
		if err := tools.Authorize(ctx, tool.Def.Function.Name, string(args)); err != nil {
			return "", err
		}
		return run(ctx, args)
	}
	return tool
}

func workspaceAllowed(cwd string, roots []string) bool {
	if len(roots) == 0 {
		return true
	}
	cwd, err := filepath.Abs(cwd)
	if err != nil {
		return false
	}
	for _, root := range roots {
		root, err = filepath.Abs(root)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(root, cwd)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func settingsFromConfigs(configs map[string]ServerConfig) LiveSettings {
	out := LiveSettings{Servers: make([]LiveServer, 0, len(configs))}
	for id, cfg := range configs {
		server := LiveServer{ID: id, Enabled: !cfg.Disabled(), Env: cloneMap(cfg.Env), Cwd: cfg.Cwd, URL: cfg.URL, Headers: cloneMap(cfg.Headers), TimeoutMs: cfg.StartupTimeout * 1000}
		if cfg.Remote() {
			server.Transport = "http"
		} else if len(cfg.Command) > 0 {
			server.Transport, server.Command, server.Args = "stdio", cfg.Command[0], append([]string(nil), cfg.Command[1:]...)
		}
		out.Servers = append(out.Servers, server)
	}
	return normalizeLiveSettings(out)
}

func normalizeLiveSettings(in LiveSettings) LiveSettings {
	out := cloneLiveSettings(in)
	seen := map[string]bool{}
	servers := out.Servers[:0]
	for _, server := range out.Servers {
		server.ID = strings.TrimSpace(server.ID)
		if server.ID == "" || seen[server.ID] {
			continue
		}
		seen[server.ID] = true
		if server.Transport == "" {
			if server.URL != "" {
				server.Transport = "http"
			} else {
				server.Transport = "stdio"
			}
		}
		servers = append(servers, server)
	}
	out.Servers = servers
	return out
}

func validateLiveSettings(settings LiveSettings) error {
	seen := map[string]bool{}
	for _, server := range settings.Servers {
		if server.ID == "" || seen[server.ID] {
			return fmt.Errorf("MCP server IDs must be non-empty and unique")
		}
		seen[server.ID] = true
		if server.Transport != "stdio" && server.Transport != "http" {
			return fmt.Errorf("MCP server %q has unsupported transport", server.ID)
		}
		if server.Enabled {
			if server.Transport == "stdio" && strings.TrimSpace(server.Command) == "" {
				return fmt.Errorf("MCP server %q requires command", server.ID)
			}
			if server.Transport == "http" && strings.TrimSpace(server.URL) == "" {
				return fmt.Errorf("MCP server %q requires url", server.ID)
			}
		}
		for _, policy := range server.Policy {
			if policy != "allow" && policy != "ask" && policy != "deny" {
				return fmt.Errorf("MCP server %q has invalid tool policy", server.ID)
			}
		}
	}
	return nil
}

func loadLiveSettings(path string) (LiveSettings, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return LiveSettings{}, err
	}
	var settings LiveSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return LiveSettings{}, fmt.Errorf("parse MCP settings: %w", err)
	}
	return settings, nil
}

func saveLiveSettings(path string, settings LiveSettings) error {
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".mcp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func redactLiveServer(server LiveServer) LiveServer {
	server.Env = nil
	server.Headers = nil
	if server.Auth != nil {
		auth := *server.Auth
		auth.CredentialRef = ""
		server.Auth = &auth
	}
	return server
}

func cloneLiveSettings(in LiveSettings) LiveSettings {
	out := LiveSettings{Selected: append([]string(nil), in.Selected...), Servers: make([]LiveServer, len(in.Servers))}
	for i, server := range in.Servers {
		out.Servers[i] = server
		out.Servers[i].Args = append([]string(nil), server.Args...)
		out.Servers[i].WorkspaceRoots = append([]string(nil), server.WorkspaceRoots...)
		out.Servers[i].Env = cloneMap(server.Env)
		out.Servers[i].Headers = cloneMap(server.Headers)
		out.Servers[i].Policy = cloneMap(server.Policy)
		if server.Auth != nil {
			auth := *server.Auth
			out.Servers[i].Auth = &auth
		}
	}
	return out
}

func cloneMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func serverKeyFor(name string) string { return serverKey(name) }
