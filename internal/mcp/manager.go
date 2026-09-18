package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/process"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

type Status int

const (
	StatusDisabled Status = iota
	StatusConnecting
	StatusReady
	StatusFailed
)

func (s Status) String() string {
	switch s {
	case StatusDisabled:
		return "disabled"
	case StatusConnecting:
		return "connecting"
	case StatusReady:
		return "ready"
	case StatusFailed:
		return "failed"
	}
	return "unknown"
}

type Server struct {
	Name   string
	Status Status
	Note   string
	Err    string
	Tools  int
	Source string
}

type server struct {
	name string
	cfg  ServerConfig

	status Status
	err    string
	note   string
	defs   []*sdkmcp.Tool
	instr  string
	sess   *sdkmcp.ClientSession
	gen    int
	stderr *ringBuffer

	ready   chan struct{}
	settled bool

	calling chan struct{}

	reconnect chan struct{}

	autoTries int

	mu sync.Mutex
}

func (s *server) disabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Disabled()
}

const autoReconnectMax = 3

func autoReconnectDelay(try int) time.Duration {
	if ms, err := strconv.Atoi(os.Getenv("K_BRAIN_TEST_MCP_BACKOFF_MS")); err == nil && ms >= 0 {
		return time.Duration(ms) * time.Millisecond
	}
	return time.Duration(1<<try) * time.Second
}

func (s *server) kickAutoReconnect(m *Manager) {
	m.onChangeMu.Lock()
	closing := m.closed
	m.onChangeMu.Unlock()
	s.mu.Lock()
	tries := s.autoTries
	s.mu.Unlock()
	if closing || s.disabled() || tries >= autoReconnectMax {
		return
	}
	go func() {
		time.Sleep(autoReconnectDelay(tries))
		m.onChangeMu.Lock()
		closing := m.closed
		m.onChangeMu.Unlock()
		s.mu.Lock()
		gave := s.status == StatusReady || s.autoTries != tries
		s.mu.Unlock()
		if closing || gave || s.disabled() {
			return
		}
		s.mu.Lock()
		s.autoTries++
		s.mu.Unlock()
		select {
		case s.reconnect <- struct{}{}:
		default:
		}
	}()
}

type Manager struct {
	servers  map[string]*server
	onChange func()

	blocked []Server

	connectTransport func(ctx context.Context, cfg ServerConfig, stderr *ringBuffer) (sdkmcp.Transport, error)

	onChangeMu sync.Mutex
	closed     bool
}

func newServer(name string, cfg ServerConfig) *server {
	s := &server{
		name:      name,
		cfg:       cfg,
		note:      cfg.Note,
		ready:     make(chan struct{}),
		calling:   make(chan struct{}, 1),
		reconnect: make(chan struct{}, 1),
	}
	if !cfg.Remote() {
		s.stderr = newRingBuffer(4096)
	}
	s.status = StatusConnecting
	if cfg.Disabled() {
		s.status = StatusDisabled
	} else if msg := cfg.Valid(); msg != "" {
		s.status = StatusFailed
		s.err = "invalid config: " + msg
	}
	if s.status != StatusConnecting {
		s.settled = true
		close(s.ready)
	}
	return s
}

func NewManager(cfgs map[string]ServerConfig) *Manager {
	m := &Manager{
		servers:          map[string]*server{},
		connectTransport: defaultTransport,
	}
	for name, cfg := range cfgs {
		m.servers[name] = newServer(name, cfg)
	}
	return m
}

func (m *Manager) AddServers(ctx context.Context, cfgs map[string]ServerConfig) {
	m.onChangeMu.Lock()
	if m.closed {
		m.onChangeMu.Unlock()
		return
	}
	var fresh []*server
	for name, cfg := range cfgs {
		if _, exists := m.servers[name]; exists {
			continue
		}
		s := newServer(name, cfg)
		m.servers[name] = s
		if s.status == StatusConnecting {
			fresh = append(fresh, s)
		}
	}
	m.onChangeMu.Unlock()
	for _, s := range fresh {
		go s.run(ctx, m)
	}
	m.fireOnChange()
}

func (m *Manager) RemoveServers(names ...string) {
	m.onChangeMu.Lock()
	var doomed []*server
	for _, name := range names {
		if s, ok := m.servers[name]; ok {
			doomed = append(doomed, s)
			delete(m.servers, name)
		}
	}
	m.onChangeMu.Unlock()
	for _, s := range doomed {
		s.mu.Lock()
		s.cfg.Enabled = new(false)
		old := s.sess
		s.sess, s.defs = nil, nil
		s.gen++
		s.mu.Unlock()
		if old != nil {
			_ = old.Close()
		}
	}
	if len(doomed) > 0 {
		m.fireOnChange()
	}
}

func (m *Manager) SetOnChange(fn func()) {
	m.onChangeMu.Lock()
	m.onChange = fn
	m.onChangeMu.Unlock()
}

func (m *Manager) FireOnChangeForTest() { m.fireOnChange() }

func (m *Manager) fireOnChange() {
	m.onChangeMu.Lock()
	fn := m.onChange
	m.onChangeMu.Unlock()
	if fn != nil {
		fn()
	}
}

func (m *Manager) Start(ctx context.Context) {
	m.onChangeMu.Lock()
	servers := make([]*server, 0, len(m.servers))
	for _, s := range m.servers {
		servers = append(servers, s)
	}
	m.onChangeMu.Unlock()
	for _, s := range servers {
		if s.status != StatusConnecting {
			continue
		}
		go s.run(ctx, m)
	}
}

func (s *server) run(ctx context.Context, m *Manager) {
	s.connect(ctx, m)
	for range s.reconnect {
		if s.disabled() {
			s.setState(m, StatusDisabled, "")
			continue
		}
		s.mu.Lock()
		ready := s.status == StatusReady
		s.mu.Unlock()
		if ready {
			continue
		}
		s.connect(context.Background(), m)
	}
}

func (s *server) connect(ctx context.Context, m *Manager) {

	s.mu.Lock()
	s.status = StatusConnecting
	s.mu.Unlock()
	m.fireOnChange()
	s.mu.Lock()
	cfg, startGen := s.cfg, s.gen
	s.mu.Unlock()
	timeout := cfg.StartupTimeoutDuration()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	transport, err := m.connectTransport(ctx, cfg, s.stderr)
	if err == nil {
		client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "k-brain", Title: "k-brain"}, nil)
		var sess *sdkmcp.ClientSession
		sess, err = client.Connect(ctx, transport, nil)
		if err == nil {
			var listed *sdkmcp.ListToolsResult
			listed, err = sess.ListTools(ctx, nil)
			if err == nil {
				m.onChangeMu.Lock()
				closed := m.closed
				_, stillOurs := m.servers[s.name]
				m.onChangeMu.Unlock()
				s.mu.Lock()
				removed := s.gen != startGen
				s.mu.Unlock()
				if closed || removed || !stillOurs {

					_ = sess.Close()
					return
				}
				var instr string
				if ir := sess.InitializeResult(); ir != nil {
					instr = strings.TrimSpace(ir.Instructions)
				}
				s.mu.Lock()
				s.defs = listed.Tools
				s.instr = instr
				s.sess = sess
				s.gen++
				s.autoTries = 0
				gen := s.gen
				s.mu.Unlock()
				s.setState(m, StatusReady, "")

				go func() {
					_ = sess.Wait()
					m.onChangeMu.Lock()
					closing := m.closed
					m.onChangeMu.Unlock()
					s.mu.Lock()
					stale := s.gen != gen
					if !stale {
						s.sess = nil
						s.defs = nil
						s.instr = ""
					}
					s.mu.Unlock()
					if !stale && !closing {
						s.setState(m, StatusFailed, "connection closed")
						s.kickAutoReconnect(m)
					}
				}()
				return
			}
			_ = sess.Close()
		}
	}
	msg := err.Error()
	if ctx.Err() == context.DeadlineExceeded {
		msg = fmt.Sprintf("timed out after %s", timeout)
	}
	if s.stderr != nil {
		if tail := strings.TrimSpace(s.stderr.String()); tail != "" {
			msg += " — stderr: " + tail
		}
	}
	s.setState(m, StatusFailed, msg)
}

func (s *server) setState(m *Manager, st Status, errMsg string) {
	s.mu.Lock()
	firstSettle := !s.settled
	if st != StatusConnecting {
		s.settled = true
	}
	s.status, s.err = st, errMsg
	s.mu.Unlock()
	if firstSettle && st != StatusConnecting {
		close(s.ready)
	}
	logf("server %s -> %s %s", s.name, st, errMsg)
	m.fireOnChange()
}

func (m *Manager) Tools() []tools.Tool {
	m.onChangeMu.Lock()
	servers := make([]*server, 0, len(m.servers))
	for _, s := range m.servers {
		servers = append(servers, s)
	}
	m.onChangeMu.Unlock()
	var out []tools.Tool
	for _, s := range servers {
		s.mu.Lock()
		defs, sess := s.defs, s.sess
		s.mu.Unlock()
		if sess == nil {
			continue
		}
		for _, d := range defs {
			out = append(out, s.bridge(d))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Def.Function.Name < out[j].Def.Function.Name })
	return out
}

func (s *server) bridge(d *sdkmcp.Tool) tools.Tool {
	name := ToolName(s.name, d.Name)
	schema := normalizeSchema(d.InputSchema)
	desc := d.Description
	if d.Title != "" && desc == "" {
		desc = d.Title
	}
	return tools.Tool{
		Def: ai.NewTool(name, fmt.Sprintf("[MCP %s] %s", s.name, desc), schema),
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			return s.call(ctx, d.Name, args)
		},
	}
}

const connectGrace = 5 * time.Second

func (s *server) call(ctx context.Context, tool string, args json.RawMessage) (string, error) {

	s.mu.Lock()
	settled, sess, status, errMsg := s.settled, s.sess, s.status, s.err
	s.mu.Unlock()
	if !settled {

		grace, cancel := context.WithTimeout(ctx, connectGrace)
		select {
		case <-s.ready:
		case <-grace.Done():
			cancel()
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "", fmt.Errorf("mcp server %q is still connecting — retry in a moment (/mcp shows status)", s.name)
		}
		cancel()
		s.mu.Lock()
		sess, status, errMsg = s.sess, s.status, s.err
		s.mu.Unlock()
	}
	if sess == nil {
		switch status {
		case StatusFailed:
			if errMsg != "" {
				return "", fmt.Errorf("mcp server %q unavailable: %s (/mcp %s reconnect)", s.name, errMsg, s.name)
			}
			return "", fmt.Errorf("mcp server %q unavailable (/mcp %s reconnect)", s.name, s.name)
		case StatusDisabled:
			return "", fmt.Errorf("mcp server %q is disabled (/mcp %s enable)", s.name, s.name)
		default:
			return "", fmt.Errorf("mcp server %q is %s", s.name, status)
		}
	}

	select {
	case s.calling <- struct{}{}:
		defer func() { <-s.calling }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.ToolTimeoutDuration())
	defer cancel()
	var argMap map[string]any
	if len(args) > 0 {
		if err := json.Unmarshal(args, &argMap); err != nil {
			return "", fmt.Errorf("invalid tool arguments: %w", err)
		}
	}
	res, err := sess.CallTool(ctx, &sdkmcp.CallToolParams{Name: tool, Arguments: argMap})
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("mcp tool %s timed out after %s", tool, s.cfg.ToolTimeoutDuration())
		}
		return "", err
	}
	return flattenResult(res), nil
}

func flattenResult(res *sdkmcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		switch c := c.(type) {
		case *sdkmcp.TextContent:
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(c.Text)
		case *sdkmcp.ImageContent:
			fmt.Fprintf(&b, "\n[image content omitted: %s, %d bytes]", c.MIMEType, len(c.Data))
		case *sdkmcp.AudioContent:
			fmt.Fprintf(&b, "\n[audio content omitted: %s, %d bytes]", c.MIMEType, len(c.Data))
		case *sdkmcp.EmbeddedResource:
			if c.Resource != nil && c.Resource.Text != "" {
				fmt.Fprintf(&b, "\n[resource %s]\n%s", c.Resource.URI, c.Resource.Text)
			} else if c.Resource != nil {
				fmt.Fprintf(&b, "\n[binary resource omitted: %s, %d bytes]", c.Resource.URI, len(c.Resource.Blob))
			}
		case *sdkmcp.ResourceLink:
			fmt.Fprintf(&b, "\n[resource link: %s (%s)]", c.URI, c.Name)
		}
	}
	out := b.String()
	if out == "" && res.StructuredContent != nil {
		if data, err := json.MarshalIndent(res.StructuredContent, "", "  "); err == nil {
			out = string(data)
		}
	}
	if out == "" {
		out = "(no output)"
	}
	if res.IsError {
		out = "Error: " + out
	}
	return tools.Truncate(out)
}

func normalizeSchema(schema any) string {
	src, ok := schema.(map[string]any)
	if !ok || src == nil {
		return `{"type":"object","properties":{}}`
	}
	m := make(map[string]any, len(src)+2)
	maps.Copy(m, src)
	m["type"] = "object"
	if _, ok := m["properties"]; !ok {
		m["properties"] = map[string]any{}
	}
	data, err := json.Marshal(m)
	if err != nil {
		return `{"type":"object","properties":{}}`
	}
	return string(data)
}

func (m *Manager) Config(name string) (ServerConfig, bool) {
	m.onChangeMu.Lock()
	s, ok := m.servers[name]
	m.onChangeMu.Unlock()
	if !ok {
		return ServerConfig{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg, true
}

func (m *Manager) Disable(name string) bool {
	m.onChangeMu.Lock()
	s, ok := m.servers[name]
	m.onChangeMu.Unlock()
	if !ok {
		return false
	}
	s.mu.Lock()
	s.cfg.Enabled = new(false)
	old := s.sess
	s.sess, s.defs = nil, nil
	s.gen++
	s.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	s.setState(m, StatusDisabled, "")
	return true
}

func (m *Manager) Enable(name string) bool {
	m.onChangeMu.Lock()
	s, ok := m.servers[name]
	m.onChangeMu.Unlock()
	if !ok {
		return false
	}
	s.mu.Lock()
	s.cfg.Enabled = nil
	s.mu.Unlock()
	return m.Reconnect(name)
}

func (m *Manager) InstructionsBlock() string {
	m.onChangeMu.Lock()
	servers := make([]*server, 0, len(m.servers))
	for _, s := range m.servers {
		servers = append(servers, s)
	}
	m.onChangeMu.Unlock()
	type entry struct{ name, text string }
	var instr []entry
	for _, s := range servers {
		s.mu.Lock()
		ready, text := s.sess != nil, s.instr
		s.mu.Unlock()
		if ready && text != "" {
			instr = append(instr, entry{s.name, text})
		}
	}
	if len(instr) == 0 {
		return ""
	}
	sort.Slice(instr, func(i, j int) bool { return instr[i].name < instr[j].name })
	var b strings.Builder
	b.WriteString("\n<mcp_instructions>\n")
	for _, e := range instr {
		fmt.Fprintf(&b, "<server name=%q>\n%s\n</server>\n", e.name, e.text)
	}
	b.WriteString("</mcp_instructions>")
	return b.String()
}

type ProbeResult struct {
	Server
	Elapsed   time.Duration
	ToolNames []string
}

func Probe(ctx context.Context, name string, cfg ServerConfig) ProbeResult {
	start := time.Now()
	m := NewManager(map[string]ServerConfig{name: cfg})
	defer m.Close()
	m.Start(ctx)
	s := m.servers[name]
	select {
	case <-s.ready:
	case <-ctx.Done():
		return ProbeResult{Server: Server{Name: name, Status: StatusFailed, Err: ctx.Err().Error()}, Elapsed: time.Since(start)}
	}
	st := m.Statuses()[0]
	res := ProbeResult{Server: st, Elapsed: time.Since(start)}
	for _, t := range m.Tools() {
		res.ToolNames = append(res.ToolNames, t.Def.Function.Name)
		if len(res.ToolNames) == 5 {
			res.ToolNames = append(res.ToolNames, "…")
			break
		}
	}
	return res
}

func (m *Manager) SetBlocked(cfgs map[string]ServerConfig) {
	m.onChangeMu.Lock()
	defer m.onChangeMu.Unlock()
	m.blocked = make([]Server, 0, len(cfgs))
	for name, c := range cfgs {
		m.blocked = append(m.blocked, Server{Name: name, Status: StatusDisabled, Note: c.Note, Source: c.Source})
	}
	sort.Slice(m.blocked, func(i, j int) bool { return m.blocked[i].Name < m.blocked[j].Name })
}

func (m *Manager) Blocked() []Server {
	m.onChangeMu.Lock()
	defer m.onChangeMu.Unlock()
	return append([]Server(nil), m.blocked...)
}

func (m *Manager) BlockedByPolicy(name string) bool {
	m.onChangeMu.Lock()
	defer m.onChangeMu.Unlock()
	for _, b := range m.blocked {
		if b.Name == name {
			return true
		}
	}
	return false
}

func (m *Manager) Statuses() []Server {
	m.onChangeMu.Lock()
	servers := make([]*server, 0, len(m.servers))
	for _, s := range m.servers {
		servers = append(servers, s)
	}
	m.onChangeMu.Unlock()
	out := make([]Server, 0, len(servers))
	for _, s := range servers {
		s.mu.Lock()
		out = append(out, Server{Name: s.name, Status: s.status, Note: s.note, Err: s.err, Tools: len(s.defs), Source: s.cfg.Source})
		s.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (m *Manager) Reconnect(name string) bool {
	m.onChangeMu.Lock()
	s, ok := m.servers[name]
	m.onChangeMu.Unlock()
	if !ok {
		return false
	}
	s.mu.Lock()
	old := s.sess
	s.sess, s.defs = nil, nil
	s.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	select {
	case s.reconnect <- struct{}{}:
	default:
	}
	return true
}

func (m *Manager) Close() {
	m.onChangeMu.Lock()
	m.closed = true
	servers := make([]*server, 0, len(m.servers))
	for _, s := range m.servers {
		servers = append(servers, s)
	}
	m.onChangeMu.Unlock()
	for _, s := range servers {
		s.mu.Lock()
		sess := s.sess
		s.sess, s.defs = nil, nil
		s.mu.Unlock()
		if sess != nil {
			_ = sess.Close()
		}
	}
}

func defaultTransport(ctx context.Context, cfg ServerConfig, stderr *ringBuffer) (sdkmcp.Transport, error) {
	if cfg.Remote() {

		headers := make(map[string]string, len(cfg.Headers))
		for k, v := range cfg.Headers {
			rv, err := config.ResolveHeader(v)
			if err != nil {
				logf("header %s: %v (dropped)", k, err)
				continue
			}
			headers[k] = rv
		}
		return &sdkmcp.StreamableClientTransport{
			Endpoint:   cfg.URL,
			HTTPClient: &http.Client{Transport: headerTransport(headers)},

			DisableStandaloneSSE: true,
		}, nil
	}

	cmd := exec.CommandContext(context.WithoutCancel(ctx), cfg.Command[0], cfg.Command[1:]...)

	env, err := config.ResolveEnvMap(cfg.Env)
	if err != nil {
		return nil, fmt.Errorf("env: %w", err)
	}

	cmd.Env = append(os.Environ(), envPairs(env)...)
	if cfg.Cwd != "" {
		cmd.Dir = cfg.Cwd
	}
	if stderr != nil {
		cmd.Stderr = stderr
	}

	process.Configure(cmd, false)
	return &sdkmcp.CommandTransport{Command: cmd, TerminateDuration: 3 * time.Second}, nil
}

func envPairs(env map[string]string) []string {
	pairs := make([]string, 0, len(env))
	for k, v := range env {
		pairs = append(pairs, k+"="+v)
	}
	return pairs
}

type headerTransport map[string]string

func (h headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	for k, v := range h {
		req.Header.Set(k, v)
	}
	return http.DefaultTransport.RoundTrip(req)
}

func logf(format string, args ...any) {
	config.LogEvent("mcp", fmt.Sprintf(format, args...))
}
