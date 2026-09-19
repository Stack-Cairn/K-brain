package lsp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/fileuri"
	"github.com/Stack-Cairn/K-brain/internal/process"
)

const diagWait = 1500 * time.Millisecond

const initTimeout = 10 * time.Second

type ServerSpec struct {
	Command     []string
	Extensions  []string
	RootMarkers []string
	Env         map[string]string
	Disabled    bool
}

var builtinServers = map[string]ServerSpec{
	"gopls": {
		Command:     []string{"gopls"},
		Extensions:  []string{".go"},
		RootMarkers: []string{"go.work", "go.mod", "go.sum"},
	},
}

func FromConfigMap(in map[string]config.LSPServer) map[string]ServerSpec {
	out := make(map[string]ServerSpec, len(builtinServers)+len(in))
	maps.Copy(out, builtinServers)
	for name, c := range in {
		existing := out[name]
		if c.Enabled != nil && !*c.Enabled {
			delete(out, name)
			continue
		}
		spec := existing
		if len(c.Command) > 0 {
			spec.Command = c.Command
		}
		if len(c.Extensions) > 0 {
			spec.Extensions = c.Extensions
		}
		if len(c.RootMarkers) > 0 {
			spec.RootMarkers = c.RootMarkers
		}
		if len(c.Env) > 0 {
			spec.Env = c.Env
		}
		out[name] = spec
	}
	return out
}

type Status struct {
	Name  string
	Root  string
	State string
	Err   string
}

type Manager struct {
	mu       sync.Mutex
	specs    map[string]ServerSpec
	clients  map[string]*clientState
	broken   map[string]string
	spawning map[string]chan struct{}
	diags    map[string][]Diagnostic
	waiters  map[string][]chan struct{}
	keyer    spawnKeyer
	closed   bool
}

type clientState struct {
	cli  *client
	cmd  *exec.Cmd
	root string
	docs map[string]int
}

type spawnKeyer func(serverID, abs string, markers []string) string

func NewManager(specs map[string]ServerSpec) *Manager {
	return &Manager{
		specs:    specs,
		clients:  map[string]*clientState{},
		broken:   map[string]string{},
		spawning: map[string]chan struct{}{},
		diags:    map[string][]Diagnostic{},
		waiters:  map[string][]chan struct{}{},
	}
}

func (m *Manager) WaitDiagnostics(ctx context.Context, path string) string {
	if m == nil {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	cs, err := m.clientFor(ctx, abs)
	if err != nil || cs == nil {
		return ""
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return ""
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ""
	}
	before, hadBefore := m.diags[abs]
	cs.docs[abs]++
	version := cs.docs[abs]
	wch := make(chan struct{})
	pushed := false
	m.waiters[abs] = append(m.waiters[abs], wch)
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.waiters, abs)
		m.mu.Unlock()
	}()

	uri := fileURI(abs)
	if version == 1 {
		cs.cli.notify("textDocument/didOpen", map[string]any{
			"textDocument": map[string]any{

				"uri": uri, "languageId": strings.TrimPrefix(filepath.Ext(abs), "."), "version": version, "text": string(data),
			},
		})
	} else {
		cs.cli.notify("textDocument/didChange", map[string]any{
			"textDocument":   map[string]any{"uri": uri, "version": version},
			"contentChanges": []map[string]any{{"text": string(data)}},
		})
	}

	deadline := time.Now().Add(diagWait)
	for {
		m.mu.Lock()
		edited, ok := m.diags[abs]
		m.mu.Unlock()

		arrived := pushed || ok != hadBefore || !diagsEqual(before, edited)
		if arrived {

			m.mu.Lock()
			wch = make(chan struct{})
			m.waiters[abs] = append(m.waiters[abs], wch)
			m.mu.Unlock()
			graceFor := min(50*time.Millisecond, time.Until(deadline))
			grace := time.NewTimer(graceFor)
			select {
			case <-wch:
				grace.Stop()
				m.mu.Lock()
				closing := m.closed
				m.mu.Unlock()
				if closing {
					return ""
				}

			case <-ctx.Done():
				grace.Stop()
				return ""
			case <-grace.C:
			}
			break
		}
		remain := time.Until(deadline)
		if remain <= 0 {
			break
		}
		timer := time.NewTimer(remain)
		select {
		case <-wch:
			timer.Stop()

			m.mu.Lock()
			closing := m.closed
			m.mu.Unlock()
			if closing {
				return ""
			}
			pushed = true

			m.mu.Lock()
			wch = make(chan struct{})
			m.waiters[abs] = append(m.waiters[abs], wch)
			m.mu.Unlock()
		case <-ctx.Done():
			timer.Stop()
			return ""
		case <-timer.C:
		}
	}

	m.mu.Lock()
	siblings := siblingErrors(abs, m.diags)
	edited := append([]Diagnostic(nil), m.diags[abs]...)
	m.mu.Unlock()
	return Report(abs, edited, siblings)
}

func diagsEqual(a, b []Diagnostic) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (m *Manager) clientFor(ctx context.Context, abs string) (*clientState, error) {
	ext := filepath.Ext(abs)
	var name string
	var spec ServerSpec
	m.mu.Lock()
	for n, s := range m.specs {
		if slices.Contains(s.Extensions, ext) {
			name, spec = n, s
		}
		if name != "" {
			break
		}
	}
	m.mu.Unlock()
	if name == "" || spec.Disabled || len(spec.Command) == 0 {
		return nil, nil
	}
	root := findRoot(filepath.Dir(abs), spec.RootMarkers)
	if m.keyer != nil {
		root = m.keyer(name, abs, spec.RootMarkers)
	}

	m.mu.Lock()
	key := name + "\x00" + root
	if cs, ok := m.clients[key]; ok {
		m.mu.Unlock()
		return cs, nil
	}
	if msg, bad := m.broken[key]; bad {
		m.mu.Unlock()
		return nil, errors.New(msg)
	}
	if ch, ok := m.spawning[key]; ok {
		m.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		if cs, ok := m.clients[key]; ok {
			return cs, nil
		}
		return nil, errors.New(m.broken[key])
	}
	ch := make(chan struct{})
	m.spawning[key] = ch
	m.mu.Unlock()

	cs, err := m.spawn(ctx, key, name, spec, root)

	m.mu.Lock()
	delete(m.spawning, key)
	if err != nil {

		if !errors.Is(err, context.Canceled) {
			m.broken[key] = err.Error()
		}
	} else {
		m.clients[key] = cs
	}
	close(ch)
	m.mu.Unlock()
	return cs, err
}

func (m *Manager) spawn(ctx context.Context, key, name string, spec ServerSpec, root string) (*clientState, error) {
	if _, err := exec.LookPath(spec.Command[0]); err != nil {
		return nil, fmt.Errorf("%s not on PATH", spec.Command[0])
	}

	cmd := exec.CommandContext(context.WithoutCancel(ctx), spec.Command[0], spec.Command[1:]...)
	cmd.Dir = root
	cmd.Env = os.Environ()
	for k, v := range spec.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	process.Configure(cmd, false)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	cs := &clientState{cmd: cmd, root: root, docs: map[string]int{}}
	cs.cli = newClient(stdin, stdout, func(uri string, version int, diags []Diagnostic) {
		m.publish(key, uri, version, diags)
	})

	initCtx, cancel := context.WithTimeout(ctx, initTimeout)
	defer cancel()

	err = cs.cli.request(initCtx, "initialize", map[string]any{
		"processId": os.Getpid(),
		"rootUri":   fileURI(root),
		"workspaceFolders": []map[string]any{
			{"name": "workspace", "uri": fileURI(root)},
		},
		"capabilities": map[string]any{
			"textDocument": map[string]any{
				"synchronization":    map[string]any{"didOpen": true, "didChange": true},
				"publishDiagnostics": map[string]any{"versionSupport": true},
			},
		},
	}, nil)
	if err != nil {
		cs.kill()
		return nil, fmt.Errorf("initialize: %w", err)
	}
	cs.cli.notify("initialized", map[string]any{})
	return cs, nil
}

func (m *Manager) publish(key, uri string, version int, diags []Diagnostic) {
	path := uriPath(uri)
	if path == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.diags[path] = diags

	for _, ch := range m.waiters[path] {
		close(ch)
	}
	delete(m.waiters, path)
}

func (m *Manager) Statuses() []Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Status, 0, len(m.specs))
	names := make([]string, 0, len(m.specs))
	for n := range m.specs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		st := Status{Name: n, State: "not started"}
		for key, cs := range m.clients {
			if strings.HasPrefix(key, n+"\x00") {
				st.State = "connected"
				st.Root = cs.root
			}
		}
		for key, msg := range m.broken {
			if strings.HasPrefix(key, n+"\x00") {
				st.State = "failed"
				st.Err = msg
			}
		}
		out = append(out, st)
	}
	return out
}

func (m *Manager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	clients := make([]*clientState, 0, len(m.clients))
	for _, cs := range m.clients {
		clients = append(clients, cs)
	}
	for wk, chans := range m.waiters {
		for _, ch := range chans {
			close(ch)
		}
		delete(m.waiters, wk)
	}
	m.mu.Unlock()
	for _, cs := range clients {
		cs.kill()
	}
}

func (cs *clientState) kill() {
	if cs.cmd == nil {
		cs.cli.shutdown()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = cs.cli.request(ctx, "shutdown", nil, nil)
	cs.cli.notify("exit", nil)
	cs.cli.shutdown()
	if c, ok := cs.cli.stdin.(io.Closer); ok {
		_ = c.Close()
	}
	if cs.cmd.Process != nil {
		_ = process.Kill(cs.cmd)
	}
	_ = cs.cmd.Wait()
}

func findRoot(dir string, markers []string) string {
	for d := dir; ; d = filepath.Dir(d) {
		for _, mkr := range markers {
			if _, err := os.Stat(filepath.Join(d, mkr)); err == nil {
				return d
			}
		}
		parent := filepath.Dir(d)
		if parent == d {
			return dir
		}
	}
}

func fileURI(path string) string {
	return fileuri.FromPath(path)
}

func uriPath(uri string) string {
	return fileuri.Path(uri)
}
