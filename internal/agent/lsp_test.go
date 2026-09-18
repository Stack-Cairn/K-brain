package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func TestLSPDiagnosticsReachModel(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "main.go")
	argsJSON, _ := json.Marshal(map[string]string{"path": target, "content": "package main\n"})

	call := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ai.Request
		json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "text/event-stream")
		call++
		if call == 1 {
			fmt.Fprintf(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"t1","type":"function","function":{"name":"write","arguments":%s}}]}}]}`+"\n\n",
				jsonString(string(argsJSON)))
		} else {
			last := req.Messages[len(req.Messages)-1]
			if last.Role != "tool" {
				t.Errorf("expected tool result, got %s", last.Role)
			}
			if !strings.Contains(last.Content, "<diagnostics file=") || !strings.Contains(last.Content, "ERROR [2:3] undefined: foo") {
				t.Errorf("tool result missing diagnostics block: %q", last.Content)
			}
			fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"fixed"},"finish_reason":"stop"}]}`+"\n\n")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	tools.LSP = stubWaiter{block: "\n\n<diagnostics file=\"" + target + "\">\nERROR [2:3] undefined: foo\n</diagnostics>"}
	defer func() { tools.LSP = nil }()

	ag := New(ai.New(srv.URL, "k"), "m", 100, "sys")
	if _, err := ag.Turn(context.Background(), "write the file", Events{}); err != nil {
		t.Fatal(err)
	}
	if call < 2 {
		t.Fatalf("loop ended after %d calls", call)
	}
}

type stubWaiter struct{ block string }

func (s stubWaiter) WaitDiagnostics(ctx context.Context, path string) string { return s.block }

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
