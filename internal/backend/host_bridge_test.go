package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// fakeHostBridge is the desktop side of the host bridge: it answers
// POST /v1/host/{command} from an in-memory terminal and tunnel state.
type fakeHostBridge struct {
	server *httptest.Server
	token  string

	mu      sync.Mutex
	calls   []fakeHostCall
	tunnels []map[string]any
	// override answers a command instead of the default state; ok=false falls through.
	override func(r *http.Request, command string, body map[string]any) (status int, envelope any, ok bool)
}

type fakeHostCall struct {
	Command string
	Auth    string
	Body    map[string]any
}

func newFakeHostBridge(t *testing.T, token string) *fakeHostBridge {
	t.Helper()
	f := &fakeHostBridge{token: token}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeHostBridge) serve(w http.ResponseWriter, r *http.Request) {
	command, ok := strings.CutPrefix(r.URL.Path, "/v1/host/")
	if r.Method != http.MethodPost || !ok {
		http.NotFound(w, r)
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	f.calls = append(f.calls, fakeHostCall{Command: command, Auth: r.Header.Get("Authorization"), Body: body})
	override := f.override
	f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if override != nil {
		if status, envelope, ok := override(r, command, body); ok {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(envelope)
			return
		}
	}
	result, err := f.answer(command, body)
	if err != nil {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": map[string]string{"code": "bad_request", "message": err.Error()}})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
}

func (f *fakeHostBridge) answer(command string, body map[string]any) (any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch command {
	case "terminal_read_tail":
		session := map[string]any{"id": "term-1", "projectPathKey": body["project_path_key"], "cwd": "/repo", "shell": "zsh", "title": "Terminal 1", "running": true}
		return map[string]any{"sessions": []any{session}, "selectedSession": session, "output": "$ npm run dev\nready on :3000", "truncated": false}, nil
	case "gateway_tunnel_state":
		return map[string]any{"revision": 3, "agentOnline": true, "relay": map[string]any{"status": "ok", "httpStatus": 200, "error": "", "checkedAt": 1, "rttMs": 12}, "tunnels": f.tunnels}, nil
	case "gateway_tunnel_create":
		input, _ := body["input"].(map[string]any)
		id := fmt.Sprintf("tun-%d", len(f.tunnels)+1)
		f.tunnels = append(f.tunnels, map[string]any{"id": id, "slug": id, "name": input["name"], "targetUrl": input["targetUrl"], "publicPath": "/t/" + id, "createdAt": len(f.tunnels) + 1, "expiresAt": 0, "activeConnections": 0, "projectPathKey": "", "local": map[string]any{"status": "unknown", "httpStatus": 0, "error": "", "checkedAt": 0, "rttMs": 0}})
		return nil, nil
	case "gateway_tunnel_close":
		id, _ := body["tunnel_id"].(string)
		for i, tunnel := range f.tunnels {
			if tunnel["id"] == id {
				f.tunnels = append(f.tunnels[:i], f.tunnels[i+1:]...)
				return nil, nil
			}
		}
		return nil, errors.New("tunnel not found")
	case "gateway_tunnel_check":
		for _, tunnel := range f.tunnels {
			tunnel["local"] = map[string]any{"status": "ok", "httpStatus": 200, "error": "", "checkedAt": 2, "rttMs": 5}
		}
		return nil, nil
	}
	return nil, fmt.Errorf("unknown command %s", command)
}

func (f *fakeHostBridge) setOverride(override func(r *http.Request, command string, body map[string]any) (int, any, bool)) {
	f.mu.Lock()
	f.override = override
	f.mu.Unlock()
}

func (f *fakeHostBridge) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, call := range f.calls {
		out = append(out, call.Command)
	}
	return out
}

func (f *fakeHostBridge) lastCall(command string) (fakeHostCall, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.calls) - 1; i >= 0; i-- {
		if f.calls[i].Command == command {
			return f.calls[i], true
		}
	}
	return fakeHostCall{}, false
}

// connect registers the fake bridge directly, as PUT /v1/host-bridge does.
func (f *fakeHostBridge) connect(s *Server, toolNames ...string) {
	if len(toolNames) == 0 {
		toolNames = hostBridgeTools
	}
	s.hostBridge.set(&hostBridgeConn{base: f.server.URL, token: f.token, tools: toolNames, client: newHostBridgeClient()})
}

func doHostBridge(t *testing.T, base, method string, body any) (int, string) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, base+"/v1/host-bridge", reader)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data)
}

func healthHostBridge(t *testing.T, base string) (hostBridgeStatus, string) {
	t.Helper()
	resp, err := http.Get(base + "/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var out struct {
		Capabilities map[string]any   `json:"capabilities"`
		HostBridge   hostBridgeStatus `json:"host_bridge"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if out.Capabilities["host_bridge"] != float64(1) {
		t.Fatalf("capabilities.host_bridge = %v", out.Capabilities["host_bridge"])
	}
	return out.HostBridge, string(data)
}

// hostToolClient answers each user turn with the next batch of tool calls and
// records the tool definitions of every request.
type hostToolClient struct {
	scriptedClient
	lock     sync.Mutex
	batches  [][]ai.ToolCall
	next     int
	requests [][]ai.Tool
}

func (c *hostToolClient) Clone() ai.Client { return c }

func (c *hostToolClient) Stream(_ context.Context, req ai.Request, text, _ func(string), _ func(string, string, string)) (ai.Message, ai.Usage, error) {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.requests = append(c.requests, append([]ai.Tool(nil), req.Tools...))
	if last := req.Messages[len(req.Messages)-1]; last.Role != "tool" && c.next < len(c.batches) {
		batch := c.batches[c.next]
		c.next++
		if len(batch) > 0 {
			return ai.Message{Role: "assistant", ToolCalls: batch}, ai.Usage{}, nil
		}
	}
	if text != nil {
		text("done")
	}
	return ai.Message{Role: "assistant", Content: "done", StopReason: ai.StopReasonStop}, ai.Usage{}, nil
}

func (c *hostToolClient) queue(calls ...ai.ToolCall) {
	c.lock.Lock()
	c.batches = append(c.batches, calls)
	c.lock.Unlock()
}

func (c *hostToolClient) lastTools() map[string]string {
	c.lock.Lock()
	defer c.lock.Unlock()
	out := map[string]string{}
	if len(c.requests) == 0 {
		return out
	}
	for _, tool := range c.requests[len(c.requests)-1] {
		out[tool.Function.Name] = string(tool.Function.Parameters)
	}
	return out
}

var hostCallSeq int

func hostCall(name, args string) ai.ToolCall {
	hostCallSeq++
	call := ai.ToolCall{ID: fmt.Sprintf("host-call-%d", hostCallSeq), Type: "function"}
	call.Function.Name, call.Function.Arguments = name, args
	return call
}

func newHostBridgeServer(t *testing.T, client ai.Client) (*Server, string, string) {
	t.Helper()
	root := t.TempDir()
	store, err := session.Open(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	factory := func(_ context.Context, _ string, selected protocol.ModelRef) (*agent.Agent, error) {
		ag := agent.New(client, selected.Model, 1024, "system")
		ag.ModelName, ag.Provider = selected.Model, selected.Provider
		return ag, nil
	}
	s, err := New(Options{Store: store, Factory: factory, EventDir: filepath.Join(root, "events"), MemoryRoot: filepath.Join(root, "memory"), Models: []protocol.ModelRef{{Provider: "fixture", Model: "fixture-model"}}, DefaultCWD: t.TempDir(), PermissionRulesDir: filepath.Join(root, "rules")})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(s)
	t.Cleanup(func() {
		httpServer.Close()
		_ = s.Close()
	})
	return s, httpServer.URL, root
}

func TestHostBridgeRegistrationLifecycle(t *testing.T) {
	_, base, _ := newHostBridgeServer(t, &hostToolClient{})
	status, _ := healthHostBridge(t, base)
	if status.Connected || len(status.Tools) != 0 {
		t.Fatalf("fresh backend status = %+v", status)
	}
	const token = "lifecycle-secret-token"
	code, body := doHostBridge(t, base, http.MethodPut, map[string]any{"url": "http://127.0.0.1:45678", "token": token, "host": "liveagent-desktop", "tools": []string{"ReadTerminal", "TunnelManager"}})
	if code != http.StatusOK || strings.Contains(body, token) || strings.Contains(body, "45678") {
		t.Fatalf("register = %d %s", code, body)
	}
	status, raw := healthHostBridge(t, base)
	if !status.Connected || !slices.Equal(status.Tools, []string{"ReadTerminal", "TunnelManager"}) {
		t.Fatalf("status after register = %+v", status)
	}
	if strings.Contains(raw, token) || strings.Contains(raw, "45678") {
		t.Fatalf("health leaks the registration: %s", raw)
	}
	// The desktop re-sends the registration on reconnect; it replaces the old one,
	// and tools K-brain does not know are ignored.
	code, _ = doHostBridge(t, base, http.MethodPut, map[string]any{"url": "http://localhost:45679", "token": token + "-2", "host": "liveagent-desktop", "tools": []string{"TunnelManager", "SomethingNew"}, "future": true})
	if code != http.StatusOK {
		t.Fatalf("re-register = %d", code)
	}
	if status, _ = healthHostBridge(t, base); !status.Connected || !slices.Equal(status.Tools, []string{"TunnelManager"}) {
		t.Fatalf("status after re-register = %+v", status)
	}
	code, body = doHostBridge(t, base, http.MethodDelete, nil)
	if code != http.StatusOK || !strings.Contains(body, `"connected":false`) {
		t.Fatalf("clear = %d %s", code, body)
	}
	if status, _ = healthHostBridge(t, base); status.Connected || len(status.Tools) != 0 {
		t.Fatalf("status after clear = %+v", status)
	}
	if code, _ = doHostBridge(t, base, http.MethodGet, nil); code != http.StatusMethodNotAllowed {
		t.Fatalf("GET = %d", code)
	}
	code, _ = doHostBridge(t, base, http.MethodPut, map[string]any{"url": "http://[::1]:45680", "token": token, "tools": []string{"ReadTerminal"}})
	if code != http.StatusOK {
		t.Fatalf("::1 register = %d", code)
	}
}

func TestHostBridgeRegistrationNeedsBackendToken(t *testing.T) {
	s := &Server{token: "backend-token"}
	server := httptest.NewServer(s)
	defer server.Close()
	if code, _ := doHostBridge(t, server.URL, http.MethodPut, map[string]any{"url": "http://127.0.0.1:1", "token": "x", "tools": []string{"ReadTerminal"}}); code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated register = %d", code)
	}
	if s.hostBridge.current() != nil {
		t.Fatal("unauthenticated request registered a bridge")
	}
}

func TestHostBridgeRejectsNonLoopbackRegistrations(t *testing.T) {
	_, base, _ := newHostBridgeServer(t, &hostToolClient{})
	for _, tc := range []struct{ url, token string }{
		{"https://127.0.0.1:4000", "t"},
		{"http://10.0.0.5:4000", "t"},
		{"http://192.168.1.2:4000", "t"},
		{"http://example.com:4000", "t"},
		{"http://localhost.example.com:4000", "t"},
		{"http://127.0.0.2:4000", "t"},
		{"http://0.0.0.0:4000", "t"},
		{"http://user:pass@127.0.0.1:4000", "t"},
		{"http://127.0.0.1:4000/prefix", "t"},
		{"http://127.0.0.1:4000/?x=1", "t"},
		{"http://127.0.0.1:99999", "t"},
		{"ws://127.0.0.1:4000", "t"},
		{"127.0.0.1:4000", "t"},
		{"", "t"},
		{"http://127.0.0.1:4000", ""},
		{"http://127.0.0.1:4000", "has space"},
	} {
		code, body := doHostBridge(t, base, http.MethodPut, map[string]any{"url": tc.url, "token": tc.token, "tools": []string{"ReadTerminal"}})
		if code != http.StatusBadRequest {
			t.Fatalf("%q/%q accepted: %d %s", tc.url, tc.token, code, body)
		}
		if tc.token != "" && strings.Contains(body, tc.token) && tc.token != "t" {
			t.Fatalf("error echoes the token: %s", body)
		}
		if status, _ := healthHostBridge(t, base); status.Connected {
			t.Fatalf("%q left a bridge registered", tc.url)
		}
	}
}

func TestHostBridgeToolsFollowConnection(t *testing.T) {
	client := &hostToolClient{}
	s, base, _ := newHostBridgeServer(t, client)
	bridge := newFakeHostBridge(t, "follow-token")
	sess := createTestSession(t, base)

	runRules(t, base, sess.ID, "normal", nil)
	before := client.lastTools()
	if _, ok := before["TunnelManager"]; ok {
		t.Fatal("TunnelManager registered without a bridge")
	}
	if !strings.Contains(before["ReadTerminal"], `"required":["session_id"]`) {
		t.Fatalf("own ReadTerminal replaced without a bridge: %s", before["ReadTerminal"])
	}

	// The bridge connects after the session loaded: the next run has the tools.
	code, _ := doHostBridge(t, base, http.MethodPut, map[string]any{"url": bridge.server.URL, "token": bridge.token, "host": "liveagent-desktop", "tools": []string{"ReadTerminal", "TunnelManager"}})
	if code != http.StatusOK {
		t.Fatalf("register = %d", code)
	}
	runRules(t, base, sess.ID, "normal", nil)
	connected := client.lastTools()
	if _, ok := connected["TunnelManager"]; !ok {
		t.Fatalf("TunnelManager missing after late connect: %v", connected)
	}
	if !strings.Contains(connected["ReadTerminal"], "terminal_id") {
		t.Fatalf("ReadTerminal is not the desktop one: %s", connected["ReadTerminal"])
	}

	// Only the advertised tools are registered.
	doHostBridge(t, base, http.MethodPut, map[string]any{"url": bridge.server.URL, "token": bridge.token, "tools": []string{"TunnelManager"}})
	runRules(t, base, sess.ID, "normal", nil)
	if tools := client.lastTools(); !strings.Contains(tools["ReadTerminal"], `"required":["session_id"]`) || tools["TunnelManager"] == "" {
		t.Fatalf("partial advertisement = %v", tools)
	}

	doHostBridge(t, base, http.MethodDelete, nil)
	runRules(t, base, sess.ID, "normal", nil)
	if _, ok := client.lastTools()["TunnelManager"]; ok {
		t.Fatal("TunnelManager still registered after disconnect")
	}

	// A bridge that drops while a run holds the tools fails their calls clearly.
	bridge.connect(s)
	rt, err := s.loadRuntimeByID(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer s.releaseRuntime(rt)
	rt.mu.Lock()
	ag := rt.agent
	rt.mu.Unlock()
	tunnel, terminal := s.tunnelManagerTool(ag), s.readTerminalHostTool(ag)
	s.hostBridge.set(nil)
	for _, run := range []func() (string, error){
		func() (string, error) { return tunnel.Run(context.Background(), json.RawMessage(`{"action":"list"}`)) },
		func() (string, error) {
			return tunnel.Run(context.Background(), json.RawMessage(`{"action":"create","targetUrl":"http://localhost:3000"}`))
		},
		func() (string, error) { return terminal.Run(context.Background(), json.RawMessage(`{}`)) },
	} {
		if _, err := run(); err == nil || !strings.Contains(err.Error(), "LiveAgent desktop is not connected") {
			t.Fatalf("call after disconnect = %v", err)
		}
	}
}

func hostTestAgent(dir string) *agent.Agent {
	ag := agent.New(&scriptedClient{}, "fixture-model", 1024, "system")
	ag.WorkingDir = dir
	return ag
}

func allowGate(ctx context.Context) context.Context {
	return tools.WithGate(ctx, func(tools.GateRequest) (tools.GateDecision, string) { return tools.GateAllowOnce, "" })
}

func TestHostBridgeReadTerminalCommand(t *testing.T) {
	s := &Server{}
	bridge := newFakeHostBridge(t, "terminal-token")
	bridge.connect(s)
	tool := s.readTerminalHostTool(hostTestAgent("/Users/a/Repo/"))
	out, err := tool.Run(allowGate(context.Background()), json.RawMessage(`{"terminal_id":" term-1 ","max_bytes":999999}`))
	if err != nil {
		t.Fatal(err)
	}
	want := "terminal_id: term-1\ntitle: Terminal 1\ncwd: /repo\nshell: zsh\nrunning: true\ntruncated: false\n\n$ npm run dev\nready on :3000"
	if out != want {
		t.Fatalf("output = %q", out)
	}
	call, _ := bridge.lastCall("terminal_read_tail")
	if call.Body["project_path_key"] != "/Users/a/Repo" || call.Body["session_id"] != "term-1" || call.Body["max_bytes"] != float64(128*1024) || call.Auth != "Bearer terminal-token" {
		t.Fatalf("request = %+v", call)
	}

	// Several terminals and none selected; empty output.
	bridge.setOverride(func(_ *http.Request, command string, body map[string]any) (int, any, bool) {
		if _, has := body["session_id"]; has {
			t.Errorf("omitted terminal_id sent session_id: %v", body)
		}
		return 200, map[string]any{"ok": true, "result": map[string]any{"sessions": []any{
			map[string]any{"id": "a", "title": "One", "cwd": "/", "shell": "sh", "running": true},
			map[string]any{"id": "b", "title": "Two", "cwd": "/", "shell": "sh", "running": false},
		}, "selectedSession": nil, "output": "", "truncated": false}}, true
	})
	out, err = tool.Run(allowGate(context.Background()), json.RawMessage(`{}`))
	if err != nil || out != "Multiple terminal sessions are available:\n- a · One · running\n- b · Two · exited\n\n(empty output)" {
		t.Fatalf("multiple = %q %v", out, err)
	}
	call, _ = bridge.lastCall("terminal_read_tail")
	if call.Body["max_bytes"] != float64(32*1024) {
		t.Fatalf("default max_bytes = %v", call.Body["max_bytes"])
	}
	bridge.setOverride(func(*http.Request, string, map[string]any) (int, any, bool) {
		return 200, map[string]any{"ok": true, "result": map[string]any{"sessions": []any{}, "selectedSession": nil, "output": "", "truncated": false}}, true
	})
	if out, err = tool.Run(allowGate(context.Background()), json.RawMessage(`{"max_bytes":10}`)); err != nil || out != "No terminal sessions are open for the current project." {
		t.Fatalf("none = %q %v", out, err)
	}
	if call, _ = bridge.lastCall("terminal_read_tail"); call.Body["max_bytes"] != float64(4*1024) {
		t.Fatalf("clamped max_bytes = %v", call.Body["max_bytes"])
	}
	// The desktop's error reaches the model.
	bridge.setOverride(func(*http.Request, string, map[string]any) (int, any, bool) {
		return 200, map[string]any{"ok": false, "error": map[string]string{"code": "outside_project", "message": "terminal session is outside the current project"}}, true
	})
	if _, err = tool.Run(allowGate(context.Background()), json.RawMessage(`{"terminal_id":"x"}`)); err == nil || err.Error() != "terminal session is outside the current project (outside_project)" {
		t.Fatalf("error = %v", err)
	}
	// No workspace: nothing to read.
	if _, err = s.readTerminalHostTool(hostTestAgent(" ")).Run(allowGate(context.Background()), json.RawMessage(`{}`)); err == nil || err.Error() != "No current project is selected." {
		t.Fatalf("no project = %v", err)
	}
}

func TestHostBridgeReadTerminalKeepsTerminalSessions(t *testing.T) {
	s := &Server{}
	bridge := newFakeHostBridge(t, "keep-token")
	bridge.connect(s)
	ag := hostTestAgent(t.TempDir())
	var forwarded string
	ag.Tools = append(ag.Tools, tools.Tool{Def: ai.NewTool("ReadTerminal", "own", `{"type":"object"}`), Run: func(_ context.Context, raw json.RawMessage) (string, error) {
		forwarded = string(raw)
		return "own terminal", nil
	}})
	s.syncHostTools(ag)
	var names []string
	for _, tool := range ag.AvailableTools() {
		names = append(names, tool.Def.Function.Name)
	}
	if count := strings.Count(strings.Join(names, ","), "ReadTerminal"); count != 1 {
		t.Fatalf("ReadTerminal registered %d times: %v", count, names)
	}
	var host tools.Tool
	for _, tool := range ag.AvailableTools() {
		if tool.Def.Function.Name == "ReadTerminal" {
			host = tool
		}
	}
	out, err := host.Run(context.Background(), json.RawMessage(`{"session_id":"term-x","max_bytes":100,"terminal_id":"ignored"}`))
	if err != nil || out != "own terminal" || forwarded != `{"max_bytes":100,"session_id":"term-x"}` {
		t.Fatalf("session_id call = %q %v forwarded %s", out, err, forwarded)
	}
	if len(bridge.commands()) != 0 {
		t.Fatalf("TerminalSession read reached the desktop: %v", bridge.commands())
	}
	s.hostBridge.set(nil)
	s.syncHostTools(ag)
	for _, tool := range ag.AvailableTools() {
		if tool.Def.Function.Name == "ReadTerminal" && tool.Def.Function.Description != "own" {
			t.Fatal("desktop ReadTerminal kept after disconnect")
		}
	}
}

func TestHostBridgeTunnelManagerCommands(t *testing.T) {
	old := tunnelCheckSettle
	tunnelCheckSettle = 10 * time.Millisecond
	t.Cleanup(func() { tunnelCheckSettle = old })
	s := &Server{}
	bridge := newFakeHostBridge(t, "tunnel-token")
	bridge.connect(s)
	tool := s.tunnelManagerTool(hostTestAgent(t.TempDir()))
	run := func(args string) (string, error) {
		return tool.Run(allowGate(context.Background()), json.RawMessage(args))
	}
	out, err := run(`{"action":"list"}`)
	if err != nil || out != "No Remote tunnels are currently registered.\nlink: online\nrelay: ok HTTP 200 12ms" {
		t.Fatalf("empty list = %q %v", out, err)
	}
	out, err = run(`{"action":"create","targetUrl":" http://localhost:3000 ","name":"web"}`)
	if err != nil || out != "Created Remote tunnel:\n- web\n  id: tun-1\n  target: http://localhost:3000\n  publicPath: /t/tun-1\n  service: unknown\n  ttl: unlimited\nlink: online\nrelay: ok HTTP 200 12ms" {
		t.Fatalf("create = %q %v", out, err)
	}
	call, _ := bridge.lastCall("gateway_tunnel_create")
	input, _ := call.Body["input"].(map[string]any)
	if input["targetUrl"] != "http://localhost:3000" || input["name"] != "web" || input["ttlSeconds"] != float64(3600) || len(input) != 3 {
		t.Fatalf("create input = %v", call.Body)
	}
	if _, err := run(`{"action":"create","targetUrl":"http://localhost:1","ttlSeconds":60}`); err == nil || err.Error() != "TunnelManager failed: TunnelManager.ttlSeconds must be 0, 900, 3600, or 14400." {
		t.Fatalf("bad ttl = %v", err)
	}
	if _, err := run(`{"action":"create"}`); err == nil || !strings.Contains(err.Error(), "targetUrl is required") {
		t.Fatalf("missing target = %v", err)
	}
	out, err = run(`{"action":"check","id":"tun-1"}`)
	if err != nil || out != "Tunnel health (sampled ~2.5s after triggering checks; probes are async and may still be settling):\nlink: online\nrelay: ok HTTP 200 12ms\n- web\n  id: tun-1\n  target: http://localhost:3000\n  publicPath: /t/tun-1\n  service: ok HTTP 200 5ms\n  ttl: unlimited" {
		t.Fatalf("check = %q %v", out, err)
	}
	if call, _ := bridge.lastCall("gateway_tunnel_check"); call.Body["tunnel_id"] != "tun-1" {
		t.Fatalf("check body = %v", call.Body)
	}
	if _, err := run(`{"action":"check"}`); err != nil {
		t.Fatal(err)
	}
	if call, _ := bridge.lastCall("gateway_tunnel_check"); len(call.Body) != 0 {
		t.Fatalf("check all body = %v", call.Body)
	}
	if _, err := run(`{"action":"check","id":"nope"}`); err == nil || err.Error() != `TunnelManager failed: No tunnel found for id "nope".` {
		t.Fatalf("check unknown = %v", err)
	}
	if out, err = run(`{"action":"close","id":"tun-1"}`); err != nil || out != "Closed Remote tunnel tun-1." {
		t.Fatalf("close = %q %v", out, err)
	}
	if call, _ := bridge.lastCall("gateway_tunnel_close"); call.Body["tunnel_id"] != "tun-1" {
		t.Fatalf("close body = %v", call.Body)
	}
	if _, err := run(`{"action":"close","id":"tun-1"}`); err == nil || err.Error() != "TunnelManager failed: tunnel not found (bad_request)" {
		t.Fatalf("close twice = %v", err)
	}
	if _, err := run(`{"action":"close"}`); err == nil || !strings.Contains(err.Error(), "id is required") {
		t.Fatalf("close without id = %v", err)
	}
	if _, err := run(`{"action":"open"}`); err == nil || !strings.Contains(err.Error(), `must be "list", "create", "close", or "check"`) {
		t.Fatalf("bad action = %v", err)
	}
	if call, _ := bridge.lastCall("gateway_tunnel_state"); len(call.Body) != 0 {
		t.Fatalf("state body = %v", call.Body)
	}
	// A desktop that answers with HTTP errors is a transport failure.
	bridge.setOverride(func(*http.Request, string, map[string]any) (int, any, bool) { return 500, map[string]any{}, true })
	if _, err := run(`{"action":"list"}`); err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("HTTP 500 = %v", err)
	}
}

func TestHostBridgeTunnelFormatting(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	for _, tc := range []struct {
		expires float64
		want    string
	}{
		{0, "unlimited"}, {999_000, "expired"}, {1_000_000 + 59, "1m"}, {1_000_000 + 3600, "1h"},
		{1_000_000 + 3600 + 61, "1h 2m"}, {1_000_000 + 4*3600 - 1, "3h"}, {1_000_000 + 2*3600 + 1800, "2h 30m"},
	} {
		if got := formatTunnelRemaining(tc.expires, now); got != tc.want {
			t.Fatalf("remaining(%v) = %q, want %q", tc.expires, got, tc.want)
		}
	}
	unsupported := true
	snapshot := hostTunnelState{Relay: &hostTunnelHealth{Status: "failed", HTTPStatus: 502, Error: "bad gateway"}, GatewayUnsupported: &unsupported}
	if got := strings.Join(formatTunnelSnapshotHealth(snapshot), "\n"); got != "link: offline\nrelay: failed HTTP 502 bad gateway\ngateway: connected gateway does not support tunnels (no public URLs)" {
		t.Fatalf("health = %q", got)
	}
	line := formatTunnelLine(hostTunnel{ID: "x", TargetURL: "http://localhost:5173", PublicPath: "t/x"}, "https://gw.example/", now)
	if line != "- http://localhost:5173\n  id: x\n  target: http://localhost:5173\n  public: https://gw.example/t/x\n  service: unknown\n  ttl: unlimited" {
		t.Fatalf("line = %q", line)
	}
}

func TestHostBridgeCallTimeoutAndCancel(t *testing.T) {
	old := hostBridgeCallTimeout
	hostBridgeCallTimeout = 200 * time.Millisecond
	t.Cleanup(func() { hostBridgeCallTimeout = old })
	s := &Server{}
	bridge := newFakeHostBridge(t, "slow-token")
	bridge.connect(s)
	bridge.setOverride(func(r *http.Request, _ string, _ map[string]any) (int, any, bool) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
		return 200, map[string]any{"ok": true}, true
	})
	tunnel := s.tunnelManagerTool(hostTestAgent(t.TempDir()))
	terminal := s.readTerminalHostTool(hostTestAgent(t.TempDir()))
	started := time.Now()
	if _, err := tunnel.Run(allowGate(context.Background()), json.RawMessage(`{"action":"list"}`)); err == nil || !strings.Contains(err.Error(), "did not answer gateway_tunnel_state within") {
		t.Fatalf("timeout = %v", err)
	}
	if _, err := terminal.Run(allowGate(context.Background()), json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "did not answer terminal_read_tail within") {
		t.Fatalf("terminal timeout = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("timeouts took %s", elapsed)
	}
	// Stopping the run cancels a call in flight.
	hostBridgeCallTimeout = 30 * time.Second
	for _, args := range []string{`{"action":"list"}`, `{"action":"create","targetUrl":"http://localhost:3000"}`, `{"action":"close","id":"t"}`, `{"action":"check"}`} {
		ctx, cancel := context.WithCancel(allowGate(context.Background()))
		time.AfterFunc(100*time.Millisecond, cancel)
		started = time.Now()
		if _, err := tunnel.Run(ctx, json.RawMessage(args)); !errors.Is(err, context.Canceled) {
			t.Fatalf("%s cancelled = %v", args, err)
		}
		if elapsed := time.Since(started); elapsed > 3*time.Second {
			t.Fatalf("%s cancel took %s", args, elapsed)
		}
	}
	// The check settle delay ends early on cancel too.
	bridge.setOverride(nil)
	tunnelCheckSettle = time.Minute
	t.Cleanup(func() { tunnelCheckSettle = 2500 * time.Millisecond })
	ctx, cancel := context.WithCancel(allowGate(context.Background()))
	time.AfterFunc(100*time.Millisecond, cancel)
	if _, err := tunnel.Run(ctx, json.RawMessage(`{"action":"check"}`)); !errors.Is(err, context.Canceled) {
		t.Fatalf("check settle cancel = %v", err)
	}
	// A desktop that is gone is reported as not connected.
	bridge.server.Close()
	if _, err := tunnel.Run(allowGate(context.Background()), json.RawMessage(`{"action":"list"}`)); err == nil || !strings.Contains(err.Error(), "LiveAgent desktop is not connected") || strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("unreachable = %v", err)
	}
}

// Every command reports the desktop's error envelope, a non-200 status, a timeout
// and a cancelled run as a failed call.
func TestHostBridgeCommandFailures(t *testing.T) {
	oldSettle, oldTimeout := tunnelCheckSettle, hostBridgeCallTimeout
	tunnelCheckSettle, hostBridgeCallTimeout = 10*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { tunnelCheckSettle, hostBridgeCallTimeout = oldSettle, oldTimeout })
	s := &Server{}
	bridge := newFakeHostBridge(t, "failure-token")
	bridge.connect(s)
	terminal := s.readTerminalHostTool(hostTestAgent(t.TempDir()))
	tunnel := s.tunnelManagerTool(hostTestAgent(t.TempDir()))
	cases := []struct {
		command string
		tool    tools.Tool
		args    string
	}{
		{"terminal_read_tail", terminal, `{}`},
		{"gateway_tunnel_state", tunnel, `{"action":"list"}`},
		{"gateway_tunnel_create", tunnel, `{"action":"create","targetUrl":"http://localhost:3000"}`},
		{"gateway_tunnel_close", tunnel, `{"action":"close","id":"tun-9"}`},
		{"gateway_tunnel_check", tunnel, `{"action":"check"}`},
	}
	for _, tc := range cases {
		// fail makes only tc.command misbehave; the others answer normally.
		fail := func(answer func(r *http.Request) (int, any)) {
			bridge.setOverride(func(r *http.Request, command string, _ map[string]any) (int, any, bool) {
				if command != tc.command {
					return 0, nil, false
				}
				status, envelope := answer(r)
				return status, envelope, true
			})
		}
		run := func(ctx context.Context) error {
			_, err := tc.tool.Run(allowGate(ctx), json.RawMessage(tc.args))
			return err
		}

		fail(func(*http.Request) (int, any) {
			return 200, map[string]any{"ok": false, "error": map[string]string{"code": "gateway_offline", "message": "gateway refused " + tc.command}}
		})
		if err := run(context.Background()); err == nil || !strings.Contains(err.Error(), "gateway refused "+tc.command+" (gateway_offline)") {
			t.Fatalf("%s error envelope = %v", tc.command, err)
		}
		fail(func(*http.Request) (int, any) { return 502, map[string]any{"ok": true} })
		if err := run(context.Background()); err == nil || !strings.Contains(err.Error(), "answered "+tc.command+" with HTTP 502") {
			t.Fatalf("%s HTTP 502 = %v", tc.command, err)
		}
		fail(func(r *http.Request) (int, any) {
			<-r.Context().Done()
			return 200, map[string]any{"ok": true}
		})
		if err := run(context.Background()); err == nil || !strings.Contains(err.Error(), "did not answer "+tc.command+" within") {
			t.Fatalf("%s timeout = %v", tc.command, err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(50*time.Millisecond, cancel)
		if err := run(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("%s cancel = %v", tc.command, err)
		}
		cancel()
	}
}

func TestHostBridgePermissionDefaults(t *testing.T) {
	client := &hostToolClient{}
	s, base, _ := newHostBridgeServer(t, client)
	bridge := newFakeHostBridge(t, "permission-token")
	bridge.connect(s)
	sess := createTestSession(t, base)

	// ReadTerminal and TunnelManager list/check run without a prompt.
	old := tunnelCheckSettle
	tunnelCheckSettle = 10 * time.Millisecond
	t.Cleanup(func() { tunnelCheckSettle = old })
	client.queue(hostCall("ReadTerminal", `{}`), hostCall("TunnelManager", `{"action":"list"}`), hostCall("TunnelManager", `{"action":"check"}`))
	events, prompts := runRules(t, base, sess.ID, "normal", nil)
	if len(prompts) != 0 {
		t.Fatalf("prompts = %+v", prompts)
	}
	for _, result := range toolResults(t, events) {
		if result.Failed {
			t.Fatalf("result failed: %+v", result)
		}
	}
	resolved := autoResolved(events)
	if len(resolved) != 3 {
		t.Fatalf("auto resolved = %+v", resolved)
	}
	commands := map[string]bool{}
	for _, r := range resolved {
		if r.Source != "read_only" {
			t.Fatalf("auto resolved = %+v", resolved)
		}
		commands[r.Tool+"("+r.Command+")"] = true
	}
	if !commands["ReadTerminal()"] || !commands["TunnelManager(list)"] || !commands["TunnelManager(check)"] {
		t.Fatalf("auto resolved commands = %v", commands)
	}

	// create and close ask; a rejected create never reaches the desktop.
	client.queue(hostCall("TunnelManager", `{"action":"create","targetUrl":"http://localhost:3000"}`))
	events, prompts = runRules(t, base, sess.ID, "normal", func(protocol.PermissionRequest) protocol.PermissionDecision {
		return protocol.PermissionDecision{Decision: "reject"}
	})
	if len(prompts) != 1 || prompts[0].Tool != "TunnelManager" || prompts[0].Command != "create http://localhost:3000" || prompts[0].Suggestion != nil {
		t.Fatalf("create prompt = %+v", prompts)
	}
	if results := toolResults(t, events); len(results) != 1 || !results[0].Failed || !strings.Contains(results[0].Output, "Permission denied") {
		t.Fatalf("rejected create = %+v", results)
	}
	if slices.Contains(bridge.commands(), "gateway_tunnel_create") {
		t.Fatal("rejected create reached the desktop")
	}
	client.queue(hostCall("TunnelManager", `{"action":"create","targetUrl":"http://localhost:3000"}`))
	_, prompts = runRules(t, base, sess.ID, "normal", func(protocol.PermissionRequest) protocol.PermissionDecision {
		return protocol.PermissionDecision{Decision: "allow_once"}
	})
	if len(prompts) != 1 || !slices.Contains(bridge.commands(), "gateway_tunnel_create") {
		t.Fatalf("approved create: prompts=%d commands=%v", len(prompts), bridge.commands())
	}
	client.queue(hostCall("TunnelManager", `{"action":"close","id":"tun-1"}`))
	_, prompts = runRules(t, base, sess.ID, "normal", func(protocol.PermissionRequest) protocol.PermissionDecision {
		return protocol.PermissionDecision{Decision: "allow_once"}
	})
	if len(prompts) != 1 || prompts[0].Command != "close tun-1" || !slices.Contains(bridge.commands(), "gateway_tunnel_close") {
		t.Fatalf("close prompts = %+v", prompts)
	}

	// Deny rules block the reads like any read tool, before the desktop is asked.
	for _, rule := range []string{"ReadTerminal", "TunnelManager(list)"} {
		resp := postJSON(t, http.DefaultClient, base+"/v1/permissions/rules", protocol.PermissionRuleCreate{Rule: rule, Effect: "deny", Scope: "session", SessionID: sess.ID}, nil)
		resp.Body.Close()
	}
	before := len(bridge.commands())
	readCall, listCall, checkCall := hostCall("ReadTerminal", `{}`), hostCall("TunnelManager", `{"action":"list"}`), hostCall("TunnelManager", `{"action":"check"}`)
	client.queue(readCall, listCall, checkCall)
	events, _ = runRules(t, base, sess.ID, "normal", nil)
	results := resultsByID(t, events)
	for _, call := range []ai.ToolCall{readCall, listCall} {
		if result := results[call.ID]; !result.Failed || !strings.Contains(result.Output, "denied by permission rule") {
			t.Fatalf("denied %s = %+v", call.Function.Arguments, result)
		}
	}
	if results[checkCall.ID].Failed {
		t.Fatalf("check matched the list rule: %+v", results[checkCall.ID])
	}
	if got := bridge.commands()[before:]; slices.Contains(got, "terminal_read_tail") || !slices.Equal(got, []string{"gateway_tunnel_check", "gateway_tunnel_state"}) {
		t.Fatalf("commands after deny rules = %v", got)
	}
}

func runUnattended(t *testing.T, s *Server, base, sessionID, requestID string) []protocol.Event {
	t.Helper()
	rt, err := s.loadRuntimeByID(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer s.releaseRuntime(rt)
	accepted, status, err := s.startCanonicalRun(withUnattendedRun(context.Background()), rt, protocol.PromptRequest{ConversationID: sessionID, ClientRequestID: requestID, Prompt: "scheduled", Options: &protocol.RunOptions{Mode: "agent", ApprovalPolicy: "ask"}})
	if err != nil || status != http.StatusAccepted {
		t.Fatalf("unattended run = %d %v", status, err)
	}
	var out []protocol.Event
	for _, event := range waitForRun(t, base, sessionID, accepted.AcceptedSeq-1) {
		if event.RunID == accepted.RunID {
			out = append(out, event)
		}
	}
	return out
}

// resultsByID indexes tool results by call ID: calls in one turn run concurrently.
func resultsByID(t *testing.T, events []protocol.Event) map[string]protocol.ToolResult {
	t.Helper()
	out := map[string]protocol.ToolResult{}
	for _, result := range toolResults(t, events) {
		out[result.ID] = result
	}
	return out
}

func TestHostBridgeUnattendedCreateNeedsAllowRule(t *testing.T) {
	client := &hostToolClient{}
	s, base, _ := newHostBridgeServer(t, client)
	bridge := newFakeHostBridge(t, "unattended-token")
	bridge.connect(s)
	sess := createTestSession(t, base)

	create, list, read := hostCall("TunnelManager", `{"action":"create","targetUrl":"http://localhost:3000"}`), hostCall("TunnelManager", `{"action":"list"}`), hostCall("ReadTerminal", `{}`)
	client.queue(create, list, read)
	started := time.Now()
	events := runUnattended(t, s, base, sess.ID, "unattended-1")
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("unattended create took %s", elapsed)
	}
	for _, event := range events {
		if event.Type == protocol.EventPermissionRequest {
			t.Fatal("unattended run asked for permission")
		}
	}
	results := resultsByID(t, events)
	// Reads run as read-only, as Read does in a scheduled run; create fails at once.
	if len(results) != 3 || !results[create.ID].Failed || !strings.Contains(results[create.ID].Output, "unattended") || results[list.ID].Failed || results[read.ID].Failed {
		t.Fatalf("unattended results = %+v", results)
	}
	if slices.Contains(bridge.commands(), "gateway_tunnel_create") {
		t.Fatal("unattended create reached the desktop without a rule")
	}

	resp := postJSON(t, http.DefaultClient, base+"/v1/permissions/rules", protocol.PermissionRuleCreate{Rule: "TunnelManager(create:*)", Effect: "allow", Scope: "user"}, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("add rule = %d", resp.StatusCode)
	}
	create, closeCall := hostCall("TunnelManager", `{"action":"create","targetUrl":"http://localhost:3000"}`), hostCall("TunnelManager", `{"action":"close","id":"tun-1"}`)
	client.queue(create, closeCall)
	results = resultsByID(t, runUnattended(t, s, base, sess.ID, "unattended-2"))
	if len(results) != 2 || results[create.ID].Failed || !results[closeCall.ID].Failed || !strings.Contains(results[closeCall.ID].Output, "unattended") {
		t.Fatalf("results with rule = %+v", results)
	}
	if !slices.Contains(bridge.commands(), "gateway_tunnel_create") || slices.Contains(bridge.commands(), "gateway_tunnel_close") {
		t.Fatalf("commands = %v", bridge.commands())
	}
}

// lockedBuffer collects log output that goroutines may still write while it is read.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestHostBridgeTokenStaysInMemory(t *testing.T) {
	const token = "kb-host-bridge-secret-7f3a9c"
	t.Setenv("HOST_BRIDGE_ENV_SENTINEL", "env-sentinel-visible")
	logs := &lockedBuffer{}
	log.SetOutput(logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	client := &hostToolClient{}
	_, base, root := newHostBridgeServer(t, client)
	bridge := newFakeHostBridge(t, token)
	sess := createTestSession(t, base)
	code, body := doHostBridge(t, base, http.MethodPut, map[string]any{"url": bridge.server.URL, "token": token, "host": "liveagent-desktop", "tools": []string{"ReadTerminal", "TunnelManager"}})
	if code != http.StatusOK {
		t.Fatalf("register = %d %s", code, body)
	}
	env := `{"command":"env"}`
	if runtime.GOOS == "windows" {
		env = `{"command":"set","shell":"cmd"}`
	}
	bash := hostCall("Bash", env)
	client.queue(bash, hostCall("ReadTerminal", `{}`), hostCall("TunnelManager", `{"action":"list"}`))
	events, _ := runRules(t, base, sess.ID, "full_access", nil)
	results := resultsByID(t, events)
	if len(results) != 3 {
		t.Fatalf("results = %+v", results)
	}
	for _, result := range results {
		if result.Failed {
			t.Fatalf("tool failed: %+v", result)
		}
	}
	if !strings.Contains(results[bash.ID].Output, "env-sentinel-visible") {
		t.Fatalf("Bash did not print the environment: %q", results[bash.ID].Output)
	}
	bridge.mu.Lock()
	calls := append([]fakeHostCall(nil), bridge.calls...)
	bridge.mu.Unlock()
	for _, call := range calls {
		if call.Auth != "Bearer "+token {
			t.Fatalf("bridge call without the token: %+v", call)
		}
	}
	secrets := []string{token, bridge.server.URL}
	check := func(where, text string) {
		t.Helper()
		for _, secret := range secrets {
			if strings.Contains(text, secret) {
				t.Fatalf("%s contains %q", where, secret)
			}
		}
	}
	check("Bash output", results[bash.ID].Output)
	for _, event := range events {
		check("event "+event.Type, string(event.Payload))
	}
	resp, err := http.Get(base + "/v1/sessions/" + sess.ID + "/history")
	if err != nil {
		t.Fatal(err)
	}
	history, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	check("history", string(history))
	_, health := healthHostBridge(t, base)
	check("health", health)
	check("log", logs.String())
	for _, kv := range os.Environ() {
		check("process environment", kv)
	}
	if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		check("file "+path, string(data))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestLiveAgentProjectPathKey(t *testing.T) {
	// Vectors from LiveAgent runtime/project_path.rs.
	for in, want := range map[string]string{
		` C:\Users\Me\Repo\ `:        "c:/users/me/repo",
		"c:/USERS/me/REPO":           "c:/users/me/repo",
		`C:\`:                        "c:/",
		`C:`:                         "c:",
		`\\Server\Share\Repo\`:       "//server/share/repo",
		`\\Server\Share\`:            "//server/share",
		`\\?\C:\Users\Me\Repo\`:      "c:/users/me/repo",
		`\\?\UNC\Server\Share\Repo\`: "//server/share/repo",
		" /Users/A/App/ ":            "/Users/A/App",
		"/tmp/Foo":                   "/tmp/Foo",
		`/tmp/Foo\`:                  `/tmp/Foo\`,
		"/":                          "/",
		"":                           "",
		`\\server`:                   `\\server`,
		`\\server\`:                  `\\server\`,
		"relative/dir/":              "relative/dir",
	} {
		if got := liveAgentProjectPathKey(in); got != want {
			t.Fatalf("liveAgentProjectPathKey(%q) = %q, want %q", in, got, want)
		}
	}
	if liveAgentProjectPathKey(`C:\Repo`) != liveAgentProjectPathKey("c:/repo/") || liveAgentProjectPathKey(`\\?\UNC\Server\Share\Repo`) != liveAgentProjectPathKey(`\\server\share\repo\`) {
		t.Fatal("equal Windows shapes differ")
	}
	if liveAgentProjectPathKey("/tmp/Foo") == liveAgentProjectPathKey("/tmp/foo") {
		t.Fatal("POSIX keys must keep case")
	}

	// ReadTerminal sends the normalized key of a Windows workspace.
	s := &Server{}
	bridge := newFakeHostBridge(t, "windows-token")
	bridge.connect(s)
	if _, err := s.readTerminalHostTool(hostTestAgent(`\\?\D:\Work\Repo\`)).Run(allowGate(context.Background()), json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if call, _ := bridge.lastCall("terminal_read_tail"); call.Body["project_path_key"] != "d:/work/repo" {
		t.Fatalf("project_path_key = %v", call.Body["project_path_key"])
	}
}
