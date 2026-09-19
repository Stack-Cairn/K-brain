package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

type fakeServer struct {
	t *testing.T

	clientToServer io.WriteCloser
	serverToClient io.ReadCloser

	mu       sync.Mutex
	opened   map[string]int
	onChange chan string
}

type push struct {
	uri     string
	version int
	diags   []Diagnostic
}

func startFakeServer(t *testing.T, script func(uri string, version int) []push) *fakeServer {
	t.Helper()
	f := &fakeServer{
		t:        t,
		opened:   map[string]int{},
		onChange: make(chan string, 64),
	}

	cStdinR, cStdinW := io.Pipe()
	cStdoutR, cStdoutW := io.Pipe()
	f.clientToServer = cStdinW
	f.serverToClient = cStdoutR
	go f.serve(cStdinR, cStdoutW, script)
	return f
}

func (f *fakeServer) serve(r io.Reader, w io.Writer, script func(string, int) []push) {

	c := &client{
		stdin: w,
		out:   make(chan []byte, 64),
		dead:  make(chan struct{}),
	}
	go c.writeLoop()
	br := bufio.NewReader(r)
	for {
		body, err := readFrame(br)
		if err != nil {
			return
		}
		var msg rpcMessage
		if err := json.Unmarshal(body, &msg); err != nil {
			continue
		}
		switch msg.Method {
		case "initialize":
			c.send(rpcMessage{ID: msg.ID, Result: json.RawMessage(`{"capabilities":{}}`)})
		case "initialized":

		case "textDocument/didOpen", "textDocument/didChange":
			var p struct {
				TextDocument struct {
					URI     string `json:"uri"`
					Version int    `json:"version"`
					Text    string `json:"text"`
				} `json:"textDocument"`
				ContentChanges []struct {
					Text string `json:"text"`
				} `json:"contentChanges"`
			}
			if err := json.Unmarshal(msg.Params, &p); err != nil {
				continue
			}
			uri, version := p.TextDocument.URI, p.TextDocument.Version
			f.mu.Lock()
			f.opened[uri] = version
			f.mu.Unlock()
			f.onChange <- uri
			if script != nil {
				for _, ps := range script(uri, version) {
					if ps.uri == "" {
						ps.uri = uri
					}
					msg := map[string]any{"uri": ps.uri, "diagnostics": diagsJSON(ps.diags)}
					if ps.version >= 0 {
						msg["version"] = ps.version
					}
					params, _ := json.Marshal(msg)
					c.send(rpcMessage{Method: "textDocument/publishDiagnostics", Params: params})
				}
			}
		default:
			if len(msg.ID) > 0 {
				c.send(rpcMessage{ID: msg.ID, Result: json.RawMessage("null")})
			}
		}
	}
}

func diagsJSON(ds []Diagnostic) []map[string]any {
	out := make([]map[string]any, len(ds))
	for i, d := range ds {
		out[i] = map[string]any{
			"range": map[string]any{
				"start": map[string]any{"line": d.Line - 1, "character": d.Col - 1},
				"end":   map[string]any{"line": d.Line - 1, "character": d.Col},
			},
			"severity": d.Severity,
			"message":  d.Message,
		}
	}
	return out
}

func fakeSpec() ServerSpec {
	return ServerSpec{Command: []string{"nonexistent-lsp-binary-xyzzy"}, Extensions: []string{".go"}, RootMarkers: []string{"go.mod"}}
}

func pipeManager(f *fakeServer) *Manager {
	m := NewManager(map[string]ServerSpec{"gopls": fakeSpec()})
	m.keyer = func(serverID, abs string, markers []string) string { return "/froot" }
	cs := &clientState{root: "/froot", docs: map[string]int{}}
	cs.cli = newClient(f.clientToServer, f.serverToClient, func(uri string, version int, diags []Diagnostic) {
		m.publish("gopls\x00/froot", uri, version, diags)
	})
	m.clients["gopls\x00/froot"] = cs
	return m
}

func TestWaitDiagnosticsEditedFile(t *testing.T) {
	f := startFakeServer(t, func(uri string, version int) []push {
		return []push{{version: version, diags: []Diagnostic{
			{Line: 3, Col: 2, Severity: SeverityError, Message: "undefined: foo"},
			{Line: 7, Col: 1, Severity: SeverityWarning, Message: "x declared and not used"},
			{Line: 9, Col: 1, Severity: SeverityHint, Message: "hint dropped"},
		}}}
	})
	m := pipeManager(f)
	defer m.Close()

	dir := t.TempDir()
	writeFile(t, dir+"/main.go", "package main\n")
	out := m.WaitDiagnostics(context.Background(), dir+"/main.go")
	want := fmt.Sprintf("\n\n<diagnostics file=%q>\nERROR [3:2] undefined: foo\nWARN [7:1] x declared and not used\n</diagnostics>", filepath.Join(dir, "main.go"))
	if out != want {
		t.Fatalf("got %q, want %q", out, want)
	}
}

func TestWaitDiagnosticsSiblingErrors(t *testing.T) {
	f := startFakeServer(t, func(uri string, version int) []push {
		return []push{
			{version: version, diags: nil},
			{uri: strings.TrimSuffix(uri, "main.go") + "other.go", version: -1, diags: []Diagnostic{
				{Line: 42, Col: 9, Severity: SeverityError, Message: "cannot use x (int) as string"},
			}},
		}
	})
	m := pipeManager(f)
	defer m.Close()

	dir := t.TempDir()
	writeFile(t, dir+"/main.go", "package main\n")
	writeFile(t, dir+"/other.go", "package main\n")
	out := m.WaitDiagnostics(context.Background(), dir+"/main.go")
	if want := fmt.Sprintf("<diagnostics file=%q>\nERROR [42:9]", filepath.Join(dir, "other.go")); !strings.Contains(out, want) {
		t.Fatalf("missing sibling block; got %q", out)
	}
	if !strings.Contains(out, "errors in file") {
		t.Fatalf("missing sibling note; got %q", out)
	}
}

func TestWaitDiagnosticsDidChangeVersions(t *testing.T) {
	f := startFakeServer(t, func(uri string, version int) []push {
		return []push{{version: version, diags: []Diagnostic{
			{Line: version, Col: 1, Severity: SeverityError, Message: fmt.Sprintf("err v%d", version)},
		}}}
	})
	m := pipeManager(f)
	defer m.Close()

	dir := t.TempDir()
	p := dir + "/main.go"
	writeFile(t, p, "package main\n")
	m.WaitDiagnostics(context.Background(), p)
	writeFile(t, p, "package main\n\n")
	out := m.WaitDiagnostics(context.Background(), p)
	if !strings.Contains(out, "err v2") {
		t.Fatalf("second touch should wait for version 2 diagnostics; got %q", out)
	}
	f.mu.Lock()
	if f.opened[fileURI(p)] != 2 {
		t.Fatalf("server saw version %d, want 2", f.opened[fileURI(p)])
	}
	f.mu.Unlock()
}

func TestWaitDiagnosticsTimeout(t *testing.T) {
	f := startFakeServer(t, func(uri string, version int) []push { return nil })
	m := pipeManager(f)
	defer m.Close()

	dir := t.TempDir()
	writeFile(t, dir+"/main.go", "package main\n")
	start := time.Now()
	out := m.WaitDiagnostics(context.Background(), dir+"/main.go")
	if out != "" {
		t.Fatalf("want empty on timeout, got %q", out)
	}
	if took := time.Since(start); took > diagWait+500*time.Millisecond {
		t.Fatalf("wait overran cap: %s", took)
	}
}

func TestWaitDiagnosticsCancel(t *testing.T) {
	f := startFakeServer(t, func(uri string, version int) []push { return nil })
	m := pipeManager(f)
	defer m.Close()

	dir := t.TempDir()
	writeFile(t, dir+"/main.go", "package main\n")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan string, 1)
	go func() { done <- m.WaitDiagnostics(ctx, dir+"/main.go") }()
	<-f.onChange
	cancel()
	select {
	case out := <-done:
		if out != "" {
			t.Fatalf("cancelled wait should return empty, got %q", out)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled wait hung")
	}
}

func TestWaitDiagnosticsUnmatchedFile(t *testing.T) {
	m := NewManager(map[string]ServerSpec{"gopls": fakeSpec()})
	defer m.Close()
	if out := m.WaitDiagnostics(context.Background(), "/tmp/readme.txt"); out != "" {
		t.Fatalf("unmatched extension should be a no-op, got %q", out)
	}
}

func TestSpawnBrokenCached(t *testing.T) {
	m := NewManager(map[string]ServerSpec{"gopls": fakeSpec()})
	defer m.Close()
	dir := t.TempDir()
	writeFile(t, dir+"/go.mod", "module x\n")
	writeFile(t, dir+"/main.go", "package main\n")
	if out := m.WaitDiagnostics(context.Background(), dir+"/main.go"); out != "" {
		t.Fatalf("broken spawn should yield no diagnostics, got %q", out)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.broken) != 1 {
		t.Fatalf("expected one broken entry, got %v", m.broken)
	}
}

func TestFromConfigMapMerge(t *testing.T) {
	disabled := false
	got := FromConfigMap(map[string]config.LSPServer{
		"gopls":  {Enabled: &disabled},
		"custom": {Command: []string{"my-lsp"}, Extensions: []string{".ml"}},
	})
	if _, ok := got["gopls"]; ok {
		t.Fatal("disabled gopls should be removed")
	}
	if got["custom"].Command[0] != "my-lsp" {
		t.Fatalf("custom server missing: %+v", got)
	}

	got = FromConfigMap(map[string]config.LSPServer{"gopls": {Command: []string{"/opt/gopls"}}})
	if got["gopls"].Extensions[0] != ".go" || got["gopls"].Command[0] != "/opt/gopls" {
		t.Fatalf("merge lost built-in defaults: %+v", got["gopls"])
	}
}

func TestRootWalk(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir+"/go.mod", "module x\n")
	writeFile(t, dir+"/sub/deep/main.go", "package main\n")
	if root := findRoot(dir+"/sub/deep", []string{"go.mod"}); root != dir {
		t.Fatalf("root = %q, want %q", root, dir)
	}
	if root := findRoot(dir+"/sub/deep", []string{"nope.marker"}); root != dir+"/sub/deep" {
		t.Fatalf("fallback root = %q, want file dir", root)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func numGoroutines() int { return runtime.NumGoroutine() }
