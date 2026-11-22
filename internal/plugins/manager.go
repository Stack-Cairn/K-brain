package plugins

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/datapath"
	"github.com/Stack-Cairn/K-brain/internal/hooks"
	"github.com/Stack-Cairn/K-brain/internal/sandbox"
	"github.com/Stack-Cairn/K-brain/internal/tools/bashrun"
)

type Manager struct {
	mu        sync.RWMutex
	plugins   map[string]Plugin
	state     map[string]bool
	statePath string
	roots     []string
	project   string
}

func Dirs(project string) []string {
	var out []string
	if project != "" {
		datapath.Report(datapath.MigrateProjectDir(project))
		out = append(out, filepath.Join(datapath.ProjectDir(project), "plugins"))
	}
	if home := pluginHome(); home != "" {
		out = append(out, filepath.Join(home, "plugins"))
	}
	return out
}

func New(project string) (*Manager, error) {
	if project != "" {
		if err := datapath.MigrateProjectDir(project); err != nil {
			return nil, err
		}
	}
	home, err := pluginHomeOrUserHome()
	if err != nil {
		return nil, err
	}
	m := &Manager{plugins: map[string]Plugin{}, state: map[string]bool{}, roots: Dirs(project), statePath: filepath.Join(home, "plugins.json"), project: project}
	if err := m.Reload(); err != nil {
		return m, err
	}
	return m, nil
}

func pluginHome() string {
	dir, err := datapath.UserDir()
	if err != nil {
		return ""
	}
	return dir
}

func pluginHomeOrUserHome() (string, error) { return datapath.UserDir() }

// Project is the workspace whose plugins this manager loads; empty when it
// loads user plugins only.
func (m *Manager) Project() string { return m.project }

// Source reports where p was found: "workspace" for the project's plugin
// directory, "user" for the user's.
func (m *Manager) Source(p Plugin) string {
	if m.project != "" {
		root := filepath.Join(datapath.ProjectDir(m.project), "plugins")
		if rel, err := filepath.Rel(root, p.Dir); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "workspace"
		}
	}
	return "user"
}

// Get returns the loaded plugin called name.
func (m *Manager) Get(name string) (Plugin, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.plugins[name]
	return p, ok
}

// loadState reads the enabled state saved by any process sharing the data
// directory (the kn CLI, the terminal UI, the backend).
func (m *Manager) loadState() map[string]bool {
	state := map[string]bool{}
	if data, err := os.ReadFile(m.statePath); err == nil {
		_ = json.Unmarshal(data, &state)
	}
	m.mu.Lock()
	m.state = state
	m.mu.Unlock()
	return maps.Clone(state)
}

// Reload rescans the plugin directories and rereads the saved enabled state.
func (m *Manager) Reload() error {
	state := m.loadState()
	found := map[string]Plugin{}
	var errs []string
	for _, root := range m.roots {
		entries, err := os.ReadDir(root)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			errs = append(errs, root+": "+err.Error())
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			p, err := Load(filepath.Join(root, entry.Name()))
			if err != nil {
				errs = append(errs, filepath.Join(root, entry.Name())+": "+err.Error())
				continue
			}
			if enabled, ok := state[p.Name]; ok {
				p.Enabled = enabled
			}
			if previous, ok := found[p.Name]; ok && previous.Dir != p.Dir {
				continue
			}
			found[p.Name] = p
		}
	}
	m.mu.Lock()
	m.plugins = found
	m.mu.Unlock()
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func (m *Manager) saveState() error {
	if err := os.MkdirAll(filepath.Dir(m.statePath), 0o700); err != nil {
		return err
	}
	m.mu.RLock()
	data, err := json.MarshalIndent(m.state, "", "  ")
	m.mu.RUnlock()
	if err != nil {
		return err
	}
	return os.WriteFile(m.statePath, append(data, '\n'), 0o600)
}

func (m *Manager) List() []Plugin {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Plugin, 0, len(m.plugins))
	for _, p := range m.plugins {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// SetEnabled saves name's enabled state on top of the state saved meanwhile
// by other managers or processes.
func (m *Manager) SetEnabled(name string, enabled bool) error {
	m.loadState()
	m.mu.Lock()
	p, ok := m.plugins[name]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("plugin %q not found", name)
	}
	p.Enabled = enabled
	m.plugins[name] = p
	m.state[name] = enabled
	m.mu.Unlock()
	return m.saveState()
}

func (m *Manager) Remove(name string) error {
	m.loadState()
	m.mu.Lock()
	p, ok := m.plugins[name]
	if ok {
		delete(m.plugins, name)
		delete(m.state, name)
	}
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("plugin %q not found", name)
	}
	if err := os.RemoveAll(p.Dir); err != nil {
		return err
	}
	return m.saveState()
}

func (m *Manager) Install(src string) error {
	src, err := filepath.Abs(src)
	if err != nil {
		return err
	}
	p, err := Load(src)
	if err != nil {
		return err
	}
	home := pluginHome()
	if home == "" {
		return errors.New("plugin directory is unavailable")
	}
	// Install where Dirs looks for user plugins.
	dst := filepath.Join(home, "plugins", p.Name)
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("plugin %q is already installed", p.Name)
	}
	if err := copyTree(src, dst); err != nil {
		_ = os.RemoveAll(dst)
		return err
	}
	return m.Reload()
}

func copyTree(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o750); err != nil {
		return err
	}
	for _, entry := range entries {
		from, to := filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			if err := copyTree(from, to); err != nil {
				return err
			}
			continue
		}
		data, err := os.ReadFile(from)
		if err != nil {
			return err
		}
		if err := os.WriteFile(to, data, 0o640); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) PromptBlock() string {
	var b strings.Builder
	for _, p := range m.List() {
		if !p.Enabled || strings.TrimSpace(p.Prompt) == "" {
			continue
		}
		fmt.Fprintf(&b, "\n<plugin name=%q version=%q>\n%s\n</plugin>\n", p.Name, p.Version, p.Prompt)
	}
	return b.String()
}

func (m *Manager) Hook(ctx context.Context, event string, payload any, policy *sandbox.Policy) error {
	return m.runHook(ctx, event, payload, policy, m.project)
}

func (m *Manager) RunHook(ctx context.Context, event hooks.Event) error {
	return m.runHook(ctx, event.Name, event, sandbox.FromContext(ctx), event.CWD)
}

func (m *Manager) runHook(ctx context.Context, event string, payload any, policy *sandbox.Policy, cwd string) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	for _, p := range m.List() {
		if !p.Enabled {
			continue
		}
		command := strings.TrimSpace(p.Hooks[event])
		if command == "" {
			continue
		}
		hctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		result := bashrun.Run(hctx, bashrun.Options{Command: command, Dir: cwd, Timeout: 30 * time.Second, Env: []string{"K_BRAIN_PLUGIN_EVENT=" + event, "K_BRAIN_PLUGIN_PAYLOAD=" + string(data)}, Sandbox: policy})
		cancel()
		if result.Exit != "" || result.TimedOut || result.Killed {
			return fmt.Errorf("plugin %s hook %s failed: %s", p.Name, event, result.Output)
		}
	}
	return nil
}

// ToolKey is plugin's part of its tools' model-facing names
// (plugin_<key>_<tool>). It is the plugin name when that has no "_", so those
// names do not change; otherwise each "_" becomes "-" and a hash of the name
// is appended, so the key never contains "_" and a name parses back to one
// plugin and the plugin's own tool name.
func ToolKey(plugin string) string {
	if !strings.Contains(plugin, "_") {
		return plugin
	}
	sum := fnv.New32a()
	_, _ = sum.Write([]byte(plugin))
	return fmt.Sprintf("%s-%08x", strings.ReplaceAll(plugin, "_", "-"), sum.Sum32())
}

// ToolName is the model-facing name of plugin's tool.
func ToolName(plugin, tool string) string { return "plugin_" + ToolKey(plugin) + "_" + tool }

// ParseToolName splits a model-facing plugin tool name into the plugin's key
// (see ToolKey) and the plugin's own tool name.
func ParseToolName(name string) (key, tool string, ok bool) {
	rest, found := strings.CutPrefix(name, "plugin_")
	if !found {
		return "", "", false
	}
	key, tool, found = strings.Cut(rest, "_")
	if !found || key == "" || tool == "" {
		return "", "", false
	}
	return key, tool, true
}

// Tools lists the enabled plugins' tools under their model-facing names.
func (m *Manager) Tools() []Tool {
	var out []Tool
	for _, p := range m.List() {
		if !p.Enabled {
			continue
		}
		for _, t := range p.Tools {
			t.Name = ToolName(p.Name, t.Name)
			out = append(out, t)
		}
	}
	return out
}

// pluginForKey finds the plugin whose ToolKey is key.
func (m *Manager) pluginForKey(key string) (Plugin, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if p, ok := m.plugins[key]; ok && ToolKey(p.Name) == key {
		return p, true
	}
	for _, p := range m.plugins {
		if ToolKey(p.Name) == key {
			return p, true
		}
	}
	return Plugin{}, false
}

type request struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type response struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code int    `json:"code"`
		Msg  string `json:"message"`
	} `json:"error"`
}

func (m *Manager) Invoke(ctx context.Context, name string, args json.RawMessage, policy *sandbox.Policy) (string, error) {
	key, toolName, ok := ParseToolName(name)
	if !ok {
		return "", fmt.Errorf("invalid plugin tool %q", name)
	}
	p, ok := m.pluginForKey(key)
	if !ok || !p.Enabled {
		return "", fmt.Errorf("plugin %q is disabled", key)
	}
	if len(p.Command) == 0 {
		return "", fmt.Errorf("plugin %q has no command", p.Name)
	}
	cmdPath := p.Command[0]
	if !filepath.IsAbs(cmdPath) {
		if strings.ContainsAny(cmdPath, `/\\`) {
			joined := filepath.Join(p.Dir, cmdPath)
			rel, relErr := filepath.Rel(p.Dir, joined)
			if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return "", fmt.Errorf("plugin %q command escapes its directory", p.Name)
			}
			cmdPath = joined
		} else if found, lookErr := exec.LookPath(cmdPath); lookErr == nil {
			cmdPath = found
		} else {
			cmdPath = filepath.Join(p.Dir, cmdPath)
		}
	}
	cmd := exec.CommandContext(ctx, cmdPath, p.Command[1:]...)
	cmd.Dir = p.Dir
	if policy == nil {
		policy = sandbox.FromContext(ctx)
	}
	if policy != nil && policy.Enabled() {
		var err error
		cmd, err = policy.Wrap(ctx, cmd)
		if err != nil {
			return "", err
		}
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	defer cmd.Process.Kill()
	req, _ := json.Marshal(request{JSONRPC: "2.0", ID: 1, Method: "tool.invoke", Params: map[string]any{"name": toolName, "arguments": json.RawMessage(args)}})
	if _, err := stdin.Write(append(req, '\n')); err != nil {
		return "", err
	}
	_ = stdin.Close()
	readCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	dataCh := make(chan []byte, 1)
	errCh := make(chan error, 1)
	go func() { data, e := bufio.NewReader(stdout).ReadBytes('\n'); dataCh <- data; errCh <- e }()
	var data []byte
	select {
	case data = <-dataCh:
		if err := <-errCh; err != nil {
			return "", err
		}
	case <-readCtx.Done():
		return "", readCtx.Err()
	}
	var resp response
	if err := json.Unmarshal(bytesTrimLine(data), &resp); err != nil {
		return "", fmt.Errorf("plugin %q invalid response: %w", p.Name, err)
	}
	if resp.Error != nil {
		return "", fmt.Errorf("plugin %q: %s", p.Name, resp.Error.Msg)
	}
	if len(resp.Result) == 0 {
		return "", nil
	}
	var result struct {
		Content string `json:"content"`
		Output  string `json:"output"`
	}
	if json.Unmarshal(resp.Result, &result) == nil {
		if result.Content != "" {
			return result.Content, nil
		}
		if result.Output != "" {
			return result.Output, nil
		}
	}
	var textResult string
	if json.Unmarshal(resp.Result, &textResult) == nil {
		return textResult, nil
	}
	return string(resp.Result), nil
}

func bytesTrimLine(data []byte) []byte { return []byte(strings.TrimSpace(string(data))) }

func (m *Manager) Platform() string { return runtime.GOOS }
