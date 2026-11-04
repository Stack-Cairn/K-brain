package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

type stubLSP struct {
	calls []string
	block string
}

func (s *stubLSP) WaitDiagnostics(ctx context.Context, path string) string {
	s.calls = append(s.calls, path)
	return s.block
}

// File tools no longer append diagnostics themselves (the agent's reminder does); they
// report the mutated path to the per-call recorder instead.
func TestFileToolsRecordMutationsWithoutInlineDiagnostics(t *testing.T) {
	stub := &stubLSP{block: "\n\n<diagnostics file=\"x.go\">\nERROR [1:1] boom\n</diagnostics>"}
	saved := LSP
	LSP = stub
	defer func() { LSP = saved }()

	dir := t.TempDir()
	p := filepath.Join(dir, "x.go")
	var recorded []string
	ctx := WithFileMutationRecorder(context.Background(), func(path string) { recorded = append(recorded, path) })

	args, _ := json.Marshal(map[string]any{"path": p, "content": "package main\n"})
	if out := Execute(ctx, All(), "write", args); strings.Contains(out, "<diagnostics") {
		t.Fatalf("write must not append diagnostics inline: %q", out)
	}
	args, _ = json.Marshal(map[string]any{"path": p, "old_string": "main", "new_string": "main2"})
	if out := Execute(ctx, All(), "edit", args); strings.Contains(out, "<diagnostics") {
		t.Fatalf("edit must not append diagnostics inline: %q", out)
	}
	if len(stub.calls) != 0 {
		t.Fatalf("file tools called the LSP directly: %v", stub.calls)
	}
	if len(recorded) != 2 || recorded[0] != p || recorded[1] != p {
		t.Fatalf("recorded = %v", recorded)
	}
}

func TestLiveAgentMutationsReachRecorderAndObserver(t *testing.T) {
	dir := t.TempDir()
	ctx := WithWorkspaceRoots(WithWorkingDir(context.Background(), dir), []WorkspaceRoot{{Path: dir, Access: "write"}})
	var observed, recorded []string
	ctx = WithFileMutationObserver(ctx, func(path string) { observed = append(observed, path) })
	ctx = WithFileMutationRecorder(ctx, func(path string) { recorded = append(recorded, path) })
	write := findToolForTest(LiveAgentCatalog(), "Write")
	if _, err := write.Run(ctx, json.RawMessage(`{"path":"a.go","content":"package a\n"}`)); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "a.go")
	if len(observed) != 1 || observed[0] != want || len(recorded) != 1 || recorded[0] != want {
		t.Fatalf("observed=%v recorded=%v", observed, recorded)
	}
}

func TestFileMutationRecorderInnermostScopeWins(t *testing.T) {
	var outer, inner []string
	ctx := WithFileMutationRecorder(context.Background(), func(p string) { outer = append(outer, p) })
	ctx = WithFileMutationRecorder(ctx, func(p string) { inner = append(inner, p) })
	BeforeFileMutation(ctx, "/x")
	if len(outer) != 0 || len(inner) != 1 {
		t.Fatalf("outer=%v inner=%v", outer, inner)
	}
}
