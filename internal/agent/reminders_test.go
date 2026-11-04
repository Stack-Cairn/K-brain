package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func reminderCall(id, name string, args any) ai.ToolCall {
	raw, _ := json.Marshal(args)
	var tc ai.ToolCall
	tc.ID, tc.Function.Name, tc.Function.Arguments = id, name, string(raw)
	return tc
}

type recordingLSP struct {
	mu    sync.Mutex
	calls []string
}

func (r *recordingLSP) WaitDiagnostics(ctx context.Context, path string) string {
	r.mu.Lock()
	r.calls = append(r.calls, path)
	r.mu.Unlock()
	return "\n\n<diagnostics file=\"" + path + "\">\nERROR [1:1] boom\n</diagnostics>"
}

func withLSP(t *testing.T, l interface {
	WaitDiagnostics(context.Context, string) string
}) {
	t.Helper()
	saved := tools.LSP
	tools.LSP = l
	t.Cleanup(func() { tools.LSP = saved })
}

func TestReminderAppendsWrappedContextWithMutatedPaths(t *testing.T) {
	withLSP(t, nil)
	dir := t.TempDir()
	target := filepath.Join(dir, "a.txt")
	var seen ToolOutcome
	ag := New(nil, "m", 100, "sys")
	ag.Reminders = append(ag.Reminders, ReminderFunc(func(ctx context.Context, call ToolOutcome) string {
		seen = call
		return "background task 7 finished"
	}), ReminderFunc(func(context.Context, ToolOutcome) string { return "  " }))

	res := ag.runTools(context.Background(), []ai.ToolCall{reminderCall("t1", "write", map[string]string{"path": target, "content": "x"})}, Events{})
	out := res[0].Text
	if !strings.HasPrefix(out, "Wrote 1 bytes") || !strings.HasSuffix(out, "\n\n<system-reminder>\nbackground task 7 finished\n</system-reminder>") {
		t.Fatalf("output = %q", out)
	}
	if strings.Count(out, "<system-reminder>") != 1 {
		t.Fatalf("blank reminder must add nothing: %q", out)
	}
	if seen.ID != "t1" || seen.Name != "write" || seen.Result.Failed || len(seen.MutatedPaths) != 1 || seen.MutatedPaths[0] != target {
		t.Fatalf("outcome = %+v", seen)
	}
	if !strings.HasPrefix(seen.Result.Text, "Wrote") || strings.Contains(seen.Result.Text, "system-reminder") {
		t.Fatalf("reminder saw %q", seen.Result.Text)
	}
}

func TestLSPReminderReportsDiagnosticsOnceForCLIEdits(t *testing.T) {
	stub := &recordingLSP{}
	withLSP(t, stub)
	dir := t.TempDir()
	target := filepath.Join(dir, "main.go")
	ag := New(nil, "m", 100, "sys")

	res := ag.runTools(context.Background(), []ai.ToolCall{reminderCall("t1", "write", map[string]string{"path": target, "content": "package main\n"})}, Events{})
	out := res[0].Text
	if strings.Count(out, "<diagnostics file=") != 1 || !strings.Contains(out, "<system-reminder>\n<diagnostics file=") {
		t.Fatalf("want one wrapped diagnostics block: %q", out)
	}
	if len(stub.calls) != 1 || stub.calls[0] != target {
		t.Fatalf("calls = %v", stub.calls)
	}

	// Read-only calls and failed edits never wait on the language server.
	ag.runTools(context.Background(), []ai.ToolCall{
		reminderCall("t2", "read", map[string]string{"path": target}),
		reminderCall("t3", "edit", map[string]string{"path": target, "old_string": "absent", "new_string": "x"}),
	}, Events{})
	if len(stub.calls) != 1 {
		t.Fatalf("unexpected LSP calls: %v", stub.calls)
	}
}

// The backend runs LiveAgent's PascalCase file tools; their edits must reach the
// reminder through the same per-call recorder.
func TestLSPReminderCoversLiveAgentEdits(t *testing.T) {
	stub := &recordingLSP{}
	withLSP(t, stub)
	dir := t.TempDir()
	target := filepath.Join(dir, "main.go")
	if err := os.WriteFile(target, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ag := New(nil, "m", 100, "sys")
	ag.WorkingDir = dir
	ag.Tools = append(ag.Tools, tools.LiveAgentCatalog()...)
	ctx := tools.WithWorkspaceRoots(tools.WithWorkingDir(context.Background(), dir), []tools.WorkspaceRoot{{Path: dir, Access: "write"}})

	res := ag.runTools(ctx, []ai.ToolCall{reminderCall("r", "Read", map[string]string{"path": "main.go"})}, Events{})
	if res[0].Failed {
		t.Fatalf("read failed: %s", res[0].Text)
	}
	res = ag.runTools(ctx, []ai.ToolCall{reminderCall("e", "Edit", map[string]string{"path": "main.go", "old_string": "package main", "new_string": "package app"})}, Events{})
	if res[0].Failed || strings.Count(res[0].Text, "<diagnostics file=") != 1 {
		t.Fatalf("edit output = %q", res[0].Text)
	}
	if len(stub.calls) != 1 || stub.calls[0] != target {
		t.Fatalf("calls = %v", stub.calls)
	}
}

func TestLSPReminderSkipsMissingFilesAndCapsWork(t *testing.T) {
	stub := &recordingLSP{}
	withLSP(t, stub)
	dir := t.TempDir()
	var paths []string
	for i := range maxDiagnosedFiles + 3 {
		p := filepath.Join(dir, string(rune('a'+i))+".go")
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	paths = append([]string{filepath.Join(dir, "deleted.go"), dir}, paths...)
	out := LSPDiagnosticsReminder{}.Remind(context.Background(), ToolOutcome{MutatedPaths: paths})
	if len(stub.calls) != maxDiagnosedFiles || stub.calls[0] != paths[2] {
		t.Fatalf("calls = %v", stub.calls)
	}
	if strings.Count(out, "<diagnostics") != maxDiagnosedFiles {
		t.Fatalf("out = %q", out)
	}
	if got := (LSPDiagnosticsReminder{}).Remind(context.Background(), ToolOutcome{MutatedPaths: paths, Result: tools.Result{Failed: true}}); got != "" {
		t.Fatalf("failed call reminded: %q", got)
	}
}

func TestSubagentInheritsReminders(t *testing.T) {
	parent := New(ai.New("http://127.0.0.1:0", "k"), "m", 100, "sys")
	marker := ReminderFunc(func(context.Context, ToolOutcome) string { return "x" })
	parent.Reminders = append(parent.Reminders, marker)
	sub := parent.newSub(SubModel{})
	if len(sub.Reminders) != len(parent.Reminders) {
		t.Fatalf("sub reminders = %d, parent = %d", len(sub.Reminders), len(parent.Reminders))
	}
}
