package plugins

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

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
		out = append(out, filepath.Join(project, ".k-brain", "plugins"))
	}
	if home := pluginHome(); home != "" {
		out = append(out, filepath.Join(home, "plugins"))
	}
	return out
}

func New(project string) (*Manager, error) {
	home, err := pluginHomeOrUserHome()
	if err != nil {
		return nil, err
	}
	m := &Manager{plugins: map[string]Plugin{}, state: map[string]bool{}, roots: Dirs(project), statePath: filepath.Join(home, "plugins.json"), project: project}
	if data, err := os.ReadFile(m.statePath); err == nil {
		_ = json.Unmarshal(data, &m.state)
	}
	if err := m.Reload(); err != nil {
		return m, err
	}
	return m, nil
}

func pluginHome() string {
	if dir := strings.TrimSpace(os.Getenv("K_BRAIN_HOME")); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".k-brain")
}

func pluginHomeOrUserHome() (string, error) {
	if dir := pluginHome(); dir != "" {
		return dir, nil
	}
	return "", fmt.Errorf("user home is unavailable")
}

func (m *Manager) Reload() error {
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
			if enabled, ok := m.state[p.Name]; ok {
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
	data, err := json.MarshalIndent(m.state, "", "  ")
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

func (m *Manager) SetEnabled(name string, enabled bool) error {
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
	dst := filepath.Join(pluginHome(), p.Name)
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

func (m *Manager) Tools() []Tool {
	var out []Tool
	for _, p := range m.List() {
		if !p.Enabled {
			continue
		}
		for _, t := range p.Tools {
			t.Name = "plugin_" + p.Name + "_" + t.Name
			out = append(out, t)
		}
	}
	return out
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
	parts := strings.SplitN(name, "_", 3)
	if len(parts) != 3 || parts[0] != "plugin" {
		return "", fmt.Errorf("invalid plugin tool %q", name)
	}
	m.mu.RLock()
	p, ok := m.plugins[parts[1]]
	m.mu.RUnlock()
	if !ok || !p.Enabled {
		return "", fmt.Errorf("plugin %q is disabled", parts[1])
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
	req, _ := json.Marshal(request{JSONRPC: "2.0", ID: 1, Method: "tool.invoke", Params: map[string]any{"name": parts[2], "arguments": json.RawMessage(args)}})
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
