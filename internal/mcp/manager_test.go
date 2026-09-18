package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func newTestServer(name string) *sdkmcp.Server {
	srv := sdkmcp.NewServer(&sdkmcp.Implementation{Name: name}, nil)
	{
		type greetIn struct {
			Name string `json:"name"`
		}
		sdkmcp.AddTool(srv, &sdkmcp.Tool{
			Name:        "greet",
			Description: "greets by name",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}},
		}, func(ctx context.Context, req *sdkmcp.CallToolRequest, in greetIn) (*sdkmcp.CallToolResult, any, error) {
			return &sdkmcp.CallToolResult{
				Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "hi " + in.Name}},
			}, nil, nil
		})
		sdkmcp.AddTool(srv, &sdkmcp.Tool{
			Name:        "fail",
			Description: "always fails",
			InputSchema: map[string]any{"type": "object"},
		}, func(ctx context.Context, req *sdkmcp.CallToolRequest, in struct{}) (*sdkmcp.CallToolResult, any, error) {
			return &sdkmcp.CallToolResult{
				Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "boom"}},
				IsError: true,
			}, nil, nil
		})
		sdkmcp.AddTool(srv, &sdkmcp.Tool{
			Name:        "structured",
			Description: "returns only structured content",
			InputSchema: map[string]any{"type": "object"},
		}, func(ctx context.Context, req *sdkmcp.CallToolRequest, in struct{}) (*sdkmcp.CallToolResult, any, error) {
			return &sdkmcp.CallToolResult{StructuredContent: map[string]any{"answer": 42}}, nil, nil
		})
		sdkmcp.AddTool(srv, &sdkmcp.Tool{
			Name:        "media",
			Description: "returns an image",
			InputSchema: map[string]any{"type": "object"},
		}, func(ctx context.Context, req *sdkmcp.CallToolRequest, in struct{}) (*sdkmcp.CallToolResult, any, error) {
			return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{
				&sdkmcp.TextContent{Text: "here you go"},
				&sdkmcp.ImageContent{MIMEType: "image/png", Data: []byte{1, 2, 3}},
			}}, nil, nil
		})
	}
	return srv
}

func waitReady(t *testing.T, m *Manager) {
	t.Helper()
	for _, s := range m.servers {
		select {
		case <-s.ready:
		case <-time.After(10 * time.Second):
			t.Fatalf("server %s never settled", s.name)
		}
	}
}

func newTestManager(t *testing.T, cfgs map[string]ServerConfig) *Manager {
	t.Helper()
	m := NewManager(cfgs)
	m.connectTransport = func(_ context.Context, cfg ServerConfig, stderr *ringBuffer) (sdkmcp.Transport, error) {
		return serveTestServer(t, cfg.Command[0]), nil
	}
	t.Cleanup(m.Close)
	return m
}

func serveTestServer(t *testing.T, name string) *sdkmcp.InMemoryTransport {
	t.Helper()
	srv := newTestServer(name)
	clientT, serverT := sdkmcp.NewInMemoryTransports()
	ss, err := srv.Connect(context.Background(), serverT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	return clientT
}

func testCfg(name string) ServerConfig {
	return ServerConfig{Command: []string{name}, StartupTimeout: 5, ToolTimeout: 5}
}

func TestManagerConnectAndCall(t *testing.T) {
	m := newTestManager(t, map[string]ServerConfig{"docs": testCfg("docs")})
	m.Start(context.Background())
	waitReady(t, m)

	ts := m.Tools()
	if len(ts) != 4 {
		t.Fatalf("expected 4 tools, got %d: %v", len(ts), toolNames(ts))
	}
	out := tools.Execute(context.Background(), ts, "mcp__docs__greet", json.RawMessage(`{"name":"k-brain"}`))
	if out != "hi k-brain" {
		t.Errorf("greet = %q", out)
	}

	st := m.Statuses()
	if len(st) != 1 || st[0].Status != StatusReady || st[0].Tools != 4 {
		t.Fatalf("status = %+v", st)
	}
}

func TestManagerToolFailuresAreToolOutput(t *testing.T) {
	m := newTestManager(t, map[string]ServerConfig{"docs": testCfg("docs")})
	m.Start(context.Background())
	waitReady(t, m)
	out := tools.Execute(context.Background(), m.Tools(), "mcp__docs__fail", nil)
	if !strings.HasPrefix(out, "Error: ") || !strings.Contains(out, "boom") {
		t.Errorf("fail = %q", out)
	}
}

func TestManagerStructuredAndMedia(t *testing.T) {
	m := newTestManager(t, map[string]ServerConfig{"docs": testCfg("docs")})
	m.Start(context.Background())
	waitReady(t, m)
	ts := m.Tools()
	out := tools.Execute(context.Background(), ts, "mcp__docs__structured", nil)
	if !strings.Contains(out, `"answer": 42`) {
		t.Errorf("structured = %q", out)
	}
	out = tools.Execute(context.Background(), ts, "mcp__docs__media", nil)
	if !strings.Contains(out, "here you go") || !strings.Contains(out, "[image content omitted: image/png, 3 bytes]") {
		t.Errorf("media = %q", out)
	}
}

func TestManagerFailedServerDegradesToErrorString(t *testing.T) {
	m := NewManager(map[string]ServerConfig{"ghost": testCfg("ghost")})

	m.connectTransport = func(_ context.Context, cfg ServerConfig, stderr *ringBuffer) (sdkmcp.Transport, error) {
		return nil, context.DeadlineExceeded
	}
	t.Cleanup(m.Close)
	m.Start(context.Background())
	waitReady(t, m)

	st := m.Statuses()
	if st[0].Status != StatusFailed || st[0].Err == "" {
		t.Fatalf("status = %+v", st)
	}
	if n := len(m.Tools()); n != 0 {
		t.Fatalf("failed server contributed %d tools", n)
	}

	s := m.servers["ghost"]
	_, err := s.call(context.Background(), "anything", nil)
	if err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Errorf("call err = %v", err)
	}
}

func TestManagerReconnect(t *testing.T) {
	m := newTestManager(t, map[string]ServerConfig{"docs": testCfg("docs")})
	m.Start(context.Background())
	waitReady(t, m)
	if st := m.Statuses(); st[0].Status != StatusReady {
		t.Fatal("expected ready")
	}

	s := m.servers["docs"]
	s.mu.Lock()
	sess := s.sess
	s.mu.Unlock()
	sess.Close()
	deadline := time.Now().Add(2 * time.Second)
	for m.Statuses()[0].Status != StatusFailed && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if st := m.Statuses(); st[0].Status != StatusFailed {
		t.Fatalf("after session close: %+v", st[0])
	}

	if !m.Reconnect("docs") {
		t.Fatal("reconnect returned false")
	}
	for m.Statuses()[0].Status != StatusReady && time.Now().Before(deadline.Add(2*time.Second)) {
		time.Sleep(10 * time.Millisecond)
	}
	if st := m.Statuses(); st[0].Status != StatusReady {
		t.Fatalf("after reconnect: %+v", st[0])
	}
	out := tools.Execute(context.Background(), m.Tools(), "mcp__docs__greet", json.RawMessage(`{"name":"back"}`))
	if out != "hi back" {
		t.Errorf("greet after reconnect = %q", out)
	}
	if m.Reconnect("nope") {
		t.Error("reconnect of unknown server should return false")
	}
}

func TestManagerParallelCallsRaceClean(t *testing.T) {
	var calls atomic.Int64
	m := newTestManager(t, map[string]ServerConfig{"a": testCfg("a"), "b": testCfg("b")})
	m.Start(context.Background())
	waitReady(t, m)
	ts := m.Tools()

	done := make(chan struct{}, 32)
	for i := range 32 {
		go func(i int) {
			name := "mcp__a__greet"
			if i%2 == 1 {
				name = "mcp__b__greet"
			}
			out := tools.Execute(context.Background(), ts, name, json.RawMessage(`{"name":"x"}`))
			if out == "hi x" {
				calls.Add(1)
			}
			done <- struct{}{}
		}(i)
	}
	for range 32 {
		<-done
	}
	if calls.Load() != 32 {
		t.Errorf("only %d/32 calls succeeded", calls.Load())
	}
}

func TestManagerCallRespectsCancel(t *testing.T) {
	m := NewManager(map[string]ServerConfig{"slowpoke": testCfg("slowpoke")})

	m.connectTransport = func(_ context.Context, cfg ServerConfig, stderr *ringBuffer) (sdkmcp.Transport, error) {
		return &hangTransport{}, nil
	}
	t.Cleanup(m.Close)
	m.Start(context.Background())

	s := m.servers["slowpoke"]
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := s.call(ctx, "greet", nil)
	if err == nil || time.Since(start) > time.Second {
		t.Errorf("call should respect ctx cancel while connecting, err=%v after %s", err, time.Since(start))
	}
}

func TestManagerCallFailFast(t *testing.T) {
	m := NewManager(map[string]ServerConfig{
		"dead":   {Command: []string{"nope-not-a-binary"}, StartupTimeout: 2},
		"off":    {Command: []string{"true"}, Enabled: new(false)},
		"wedged": {Command: []string{"wedged"}, StartupTimeout: 30},
	})
	m.connectTransport = func(_ context.Context, cfg ServerConfig, stderr *ringBuffer) (sdkmcp.Transport, error) {
		if cfg.Command[0] == "wedged" {
			return &hangTransport{}, nil
		}
		return nil, errors.New("spawn failed: no such binary")
	}
	t.Cleanup(m.Close)
	m.Start(context.Background())

	<-m.servers["dead"].ready
	start := time.Now()
	_, err := m.servers["dead"].call(context.Background(), "x", nil)
	if err == nil || !strings.Contains(err.Error(), "unavailable") || !strings.Contains(err.Error(), "/mcp dead reconnect") {
		t.Errorf("failed-server call err = %v", err)
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Errorf("failed-server call blocked %s", time.Since(start))
	}

	_, err = m.servers["off"].call(context.Background(), "x", nil)
	if err == nil || !strings.Contains(err.Error(), "disabled") || !strings.Contains(err.Error(), "/mcp off enable") {
		t.Errorf("disabled-server call err = %v", err)
	}

	start = time.Now()
	_, err = m.servers["wedged"].call(context.Background(), "x", nil)
	if err == nil || !strings.Contains(err.Error(), "still connecting") {
		t.Errorf("connecting-server call err = %v", err)
	}
	if d := time.Since(start); d < connectGrace-500*time.Millisecond || d > connectGrace+2*time.Second {
		t.Errorf("connecting-server grace = %s, want ~%s", d, connectGrace)
	}
}

func TestManagerAutoReconnect(t *testing.T) {
	t.Setenv("K_BRAIN_TEST_MCP_BACKOFF_MS", "20")

	m := newTestManager(t, map[string]ServerConfig{"docs": testCfg("docs")})
	m.Start(context.Background())
	waitReady(t, m)
	if st := m.Statuses(); st[0].Status != StatusReady {
		t.Fatal("expected ready")
	}

	s := m.servers["docs"]
	s.mu.Lock()
	sess := s.sess
	s.mu.Unlock()
	sess.Close()

	s.mu.Lock()
	droppedGen := s.gen
	s.mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	recovered := false
	for time.Now().Before(deadline) {
		s.mu.Lock()
		recovered = s.status == StatusReady && s.sess != nil && s.gen > droppedGen
		s.mu.Unlock()
		if recovered {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !recovered {
		t.Fatalf("auto-reconnect did not recover: %+v", m.Statuses()[0])
	}
	out := tools.Execute(context.Background(), m.Tools(), "mcp__docs__greet", json.RawMessage(`{"name":"auto"}`))
	if out != "hi auto" {
		t.Errorf("call after auto-reconnect = %q", out)
	}
}

func TestManagerAutoReconnectGivesUp(t *testing.T) {
	t.Setenv("K_BRAIN_TEST_MCP_BACKOFF_MS", "10")

	var connects atomic.Int64
	m := NewManager(map[string]ServerConfig{"flaky": testCfg("flaky")})
	m.connectTransport = func(_ context.Context, cfg ServerConfig, stderr *ringBuffer) (sdkmcp.Transport, error) {
		connects.Add(1)
		if connects.Load() == 1 {
			return serveTestServer(t, "flaky"), nil
		}
		return nil, errors.New("server keeps dying")
	}
	t.Cleanup(m.Close)
	m.Start(context.Background())
	waitReady(t, m)

	s := m.servers["flaky"]
	s.mu.Lock()
	sess := s.sess
	s.mu.Unlock()
	sess.Close()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		tries := s.autoTries
		s.mu.Unlock()
		if tries >= autoReconnectMax {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	time.Sleep(200 * time.Millisecond)
	st := m.Statuses()[0]
	if st.Status != StatusFailed {
		t.Errorf("flaky should end failed, got %v", st.Status)
	}
	if got := connects.Load(); got > int64(autoReconnectMax)+1 {
		t.Errorf("connect attempts = %d, want <= initial + %d retries", got, autoReconnectMax)
	}
}

type hangTransport struct{}

func (hangTransport) Connect(ctx context.Context) (sdkmcp.Connection, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func toolNames(ts []tools.Tool) []string {
	var out []string
	for _, t := range ts {
		out = append(out, t.Def.Function.Name)
	}
	return out
}

func TestNormalizeSchema(t *testing.T) {
	got := normalizeSchema(nil)
	if got != `{"type":"object","properties":{}}` {
		t.Errorf("nil schema = %s", got)
	}
	got = normalizeSchema(map[string]any{"title": "x"})
	var m map[string]any
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatal(err)
	}
	if m["type"] != "object" || m["properties"] == nil || m["title"] != "x" {
		t.Errorf("coerced schema = %v", m)
	}
}

func TestNormalizeSchemaDoesNotMutateSharedInput(t *testing.T) {
	shared := map[string]any{"title": "tool", "properties": map[string]any{"a": 1}}

	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			for range 200 {
				_ = normalizeSchema(shared)
			}
		})
	}
	wg.Wait()
	if _, mutated := shared["type"]; mutated {
		t.Fatalf("normalizeSchema mutated the shared input map: %v", shared)
	}
}

func TestFlattenTruncates(t *testing.T) {
	big := strings.Repeat("x", 60_000)
	res := &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: big}}}
	out := flattenResult(res)
	if len(out) > 60_0000 || !strings.Contains(out, "bytes elided from the middle") {
		t.Errorf("truncation missing, len=%d", len(out))
	}
}

func TestServerInstructions(t *testing.T) {

	srvWithInstr := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "docs"}, &sdkmcp.ServerOptions{Instructions: "Call ping to check liveness. Always pass a name."})
	sdkmcp.AddTool(srvWithInstr, &sdkmcp.Tool{Name: "ping", InputSchema: map[string]any{"type": "object"}},
		func(ctx context.Context, req *sdkmcp.CallToolRequest, in struct{}) (*sdkmcp.CallToolResult, any, error) {
			return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "pong"}}}, nil, nil
		})

	m := NewManager(map[string]ServerConfig{"docs": testCfg("docs"), "plain": testCfg("plain")})
	m.connectTransport = func(_ context.Context, cfg ServerConfig, stderr *ringBuffer) (sdkmcp.Transport, error) {
		if cfg.Command[0] == "docs" {
			ct, st := sdkmcp.NewInMemoryTransports()
			ss, err := srvWithInstr.Connect(context.Background(), st, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { ss.Close() })
			return ct, nil
		}
		return serveTestServer(t, cfg.Command[0]), nil
	}
	t.Cleanup(m.Close)
	m.Start(context.Background())
	waitReady(t, m)

	block := m.InstructionsBlock()
	if !strings.Contains(block, `<server name="docs">`) || !strings.Contains(block, "Call ping to check liveness") {
		t.Errorf("block missing docs instructions:\n%s", block)
	}
	if strings.Contains(block, `"plain"`) {
		t.Errorf("server without instructions must not appear:\n%s", block)
	}

	s := m.servers["docs"]
	s.mu.Lock()
	sess := s.sess
	s.mu.Unlock()
	sess.Close()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		gone := s.sess == nil
		s.mu.Unlock()
		if gone {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()

		terminal := (s.status == StatusReady && s.sess != nil) || (s.status == StatusFailed && s.autoTries >= autoReconnectMax)
		s.mu.Unlock()
		if terminal {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	s.mu.Lock()
	st, instr := s.status, s.instr
	s.mu.Unlock()
	if st == StatusFailed && instr != "" {
		t.Error("failed server must not keep instructions")
	}
	if st == StatusReady && instr == "" {
		t.Error("reconnected server should restore instructions")
	}
}

func TestProbe(t *testing.T) {

	res := Probe(context.Background(), "ghost", ServerConfig{Command: []string{"no-such-binary-xyz"}, StartupTimeout: 3})
	if res.Status != StatusFailed {
		t.Errorf("ghost probe = %+v", res)
	}
	if res.Elapsed <= 0 {
		t.Error("probe should report elapsed time")
	}

	if testing.Short() {
		return
	}

	res = Probe(context.Background(), "silent", ServerConfig{Command: []string{"sh", "-c", "sleep 5"}, StartupTimeout: 1})
	if res.Status != StatusFailed || res.Err == "" {
		t.Errorf("silent probe = %+v", res)
	}
	if res.Elapsed > 15*time.Second {
		t.Errorf("probe blew far past the startup timeout: %s", res.Elapsed)
	}
}

func TestStatusString(t *testing.T) {
	want := map[Status]string{
		StatusDisabled:   "disabled",
		StatusConnecting: "connecting",
		StatusReady:      "ready",
		StatusFailed:     "failed",
		Status(99):       "unknown",
	}
	for s, w := range want {
		if got := s.String(); got != w {
			t.Errorf("Status(%d).String() = %q, want %q", s, got, w)
		}
	}
}

func TestSetOnChange(t *testing.T) {
	m := NewManager(nil)
	m.FireOnChangeForTest()
	fired := 0
	m.SetOnChange(func() { fired++ })
	m.FireOnChangeForTest()
	m.FireOnChangeForTest()
	if fired != 2 {
		t.Fatalf("callback fired %d times, want 2", fired)
	}
}
