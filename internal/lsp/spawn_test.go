package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/process"
)

func kill0(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	defer p.Release()
	if process.Alive(&exec.Cmd{Process: p}) {
		return nil
	}
	return os.ErrProcessDone
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_LSP_FAKE") != "1" {
		return
	}

	br := bufio.NewReader(os.Stdin)
	write := func(msg rpcMessage) {
		body, _ := json.Marshal(msg)
		fmt.Fprintf(os.Stdout, "Content-Length: %d\r\n\r\n%s", len(body), body)
	}
	for {
		body, err := readFrame(br)
		if err != nil {
			os.Exit(0)
		}
		var msg rpcMessage
		if json.Unmarshal(body, &msg) != nil {
			continue
		}
		switch msg.Method {
		case "initialize":
			write(rpcMessage{ID: msg.ID, Result: json.RawMessage(`{"capabilities":{}}`)})
		case "textDocument/didOpen":
			var p struct {
				TextDocument struct {
					URI     string `json:"uri"`
					Version int    `json:"version"`
				} `json:"textDocument"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			params, _ := json.Marshal(map[string]any{
				"uri":     p.TextDocument.URI,
				"version": p.TextDocument.Version,
				"diagnostics": diagsJSON([]Diagnostic{
					{Line: 1, Col: 1, Severity: SeverityError, Message: "from real process"},
				}),
			})
			write(rpcMessage{Method: "textDocument/publishDiagnostics", Params: params})
		default:
			if len(msg.ID) > 0 {
				write(rpcMessage{ID: msg.ID, Result: json.RawMessage("null")})
			}
		}
	}
}

func fakeExecSpec() ServerSpec {
	return ServerSpec{
		Command:     []string{os.Args[0], "-test.run=^TestHelperProcess$"},
		Extensions:  []string{".go"},
		RootMarkers: []string{"go.mod"},
		Env:         map[string]string{"GO_LSP_FAKE": "1"},
	}
}

func TestRealProcessLifecycle(t *testing.T) {
	m := NewManager(map[string]ServerSpec{"fake": fakeExecSpec()})

	dir := t.TempDir()
	writeFile(t, dir+"/go.mod", "module x\n")
	writeFile(t, dir+"/main.go", "package main\n")

	before := runtime.NumGoroutine()
	out := m.WaitDiagnostics(context.Background(), dir+"/main.go")
	if out == "" || !strings.Contains(out, "from real process") {
		t.Fatalf("real-process diagnostics missing: %q", out)
	}

	m.mu.Lock()
	var pid int
	for _, cs := range m.clients {
		if cs.cmd != nil && cs.cmd.Process != nil {
			pid = cs.cmd.Process.Pid
		}
	}
	m.mu.Unlock()
	if pid == 0 {
		t.Fatal("no spawned client")
	}
	m.Close()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := kill0(pid); err != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := kill0(pid); err == nil {
		t.Fatalf("process %d survived Close", pid)
	}
	time.Sleep(100 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > before+2 {
		t.Fatalf("goroutines grew across spawn+close: before=%d after=%d", before, after)
	}

	if sts := m.Statuses(); len(sts) != 1 || sts[0].Name != "fake" {
		t.Fatalf("statuses after close: %+v", sts)
	}
}
