package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestShellToolResultsCarryExitCodeAndDuration(t *testing.T) {
	ctx := WithWorkingDir(context.Background(), t.TempDir())
	for _, tc := range []struct {
		name, tool, args string
		code             int
	}{
		{"bash success", "bash", `{"command":"echo ok"}`, 0},
		{"bash nonzero", "bash", `{"command":"exit 3"}`, 3},
		{"Bash success", "Bash", `{"command":"echo ok"}`, 0},
		{"Bash nonzero", "Bash", `{"command":"exit 7"}`, 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := All()
			if tc.tool == "Bash" {
				ts = LiveAgentCatalog()
			}
			before := Execute(ctx, ts, tc.tool, json.RawMessage(tc.args))
			result := ExecuteResult(ctx, ts, tc.tool, json.RawMessage(tc.args), false)
			if result.ExitCode == nil || *result.ExitCode != tc.code {
				t.Fatalf("exit code=%v, want %d: %+v", result.ExitCode, tc.code, result)
			}
			if result.TimedOut || result.Duration <= 0 || result.DurationMs() < 1 {
				t.Fatalf("timed_out=%v duration=%v", result.TimedOut, result.Duration)
			}
			// The text the model sees is what it was before the metadata existed.
			if result.Text != before {
				t.Fatalf("text changed: %q vs %q", result.Text, before)
			}
		})
	}
}

func TestShellToolTimeoutAndCancelHaveNoExitCode(t *testing.T) {
	ctx := WithWorkingDir(context.Background(), t.TempDir())
	timeouts := []struct {
		tool, args string
		ts         []Tool
	}{
		{"bash", `{"command":"sleep 5","timeout":0.3}`, All()},
		{"Bash", `{"command":"sleep 5","timeout_ms":1000}`, LiveAgentCatalog()},
	}
	for _, tc := range timeouts {
		result := ExecuteResult(ctx, tc.ts, tc.tool, json.RawMessage(tc.args), false)
		if !result.TimedOut || result.ExitCode != nil || result.Duration <= 0 {
			t.Fatalf("%s timeout: %+v", tc.tool, result)
		}
	}

	cancelled, cancel := context.WithCancel(ctx)
	time.AfterFunc(200*time.Millisecond, cancel)
	result := ExecuteResult(cancelled, All(), "bash", json.RawMessage(`{"command":"sleep 5"}`), false)
	if !result.Cancelled || result.ExitCode != nil || result.TimedOut || result.Duration <= 0 {
		t.Fatalf("cancelled: %+v", result)
	}
}

func TestNonShellToolHasDurationWithoutExitCode(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := WithWorkingDir(context.Background(), dir)
	result := ExecuteResult(ctx, All(), "read", json.RawMessage(`{"path":"a.txt"}`), false)
	if result.ExitCode != nil || result.TimedOut || result.Duration <= 0 {
		t.Fatalf("read: %+v", result)
	}
	unknown := ExecuteResult(ctx, All(), "nope", json.RawMessage(`{}`), false)
	if unknown.ExitCode != nil || unknown.Duration != 0 {
		t.Fatalf("unknown tool ran: %+v", unknown)
	}
}

func TestToolDurationLeavesOutPermissionWait(t *testing.T) {
	const wait = 600 * time.Millisecond
	ctx := WithGate(WithWorkingDir(context.Background(), t.TempDir()), func(GateRequest) (GateDecision, string) {
		time.Sleep(wait)
		return GateAllowOnce, ""
	})
	start := time.Now()
	result := ExecuteResult(ctx, All(), "bash", json.RawMessage(`{"command":"echo ok"}`), false)
	if time.Since(start) < wait {
		t.Fatal("gate was not asked")
	}
	if result.Duration <= 0 || result.Duration >= wait {
		t.Fatalf("duration %v includes the permission wait", result.Duration)
	}
}

func TestManagedProcessWaitReportsExitCode(t *testing.T) {
	root := t.TempDir()
	ctx := WithWorkspaceRoots(WithWorkingDir(context.Background(), root), []WorkspaceRoot{{Path: root, Access: "write"}})
	catalog := LiveAgentCatalog()
	start := ExecuteResult(ctx, catalog, "ManagedProcess", json.RawMessage(`{"action":"start","command":"exit 4"}`), false)
	if start.ExitCode != nil {
		t.Fatalf("start reported an exit code: %+v", start)
	}
	id := fieldValue(start.Text, "process_id")
	result := ExecuteResult(ctx, catalog, "ProcessWait", mustJSON(map[string]any{"session_id": id, "yield_time_ms": 5000}), false)
	for i := 0; i < 50 && result.ExitCode == nil; i++ {
		time.Sleep(50 * time.Millisecond)
		result = ExecuteResult(ctx, catalog, "ProcessWait", mustJSON(map[string]any{"session_id": id, "yield_time_ms": 100}), false)
	}
	if result.ExitCode == nil || *result.ExitCode != 4 || !strings.Contains(result.Text, "exit_code=4") {
		t.Fatalf("wait: %+v", result)
	}
}

func gatePreview(t *testing.T, ctx context.Context, ts []Tool, tool, args string) *EditPreview {
	t.Helper()
	var got *GateRequest
	ctx = WithGate(ctx, func(req GateRequest) (GateDecision, string) {
		if got == nil {
			got = &req
		}
		return GateAllowOnce, ""
	})
	result := ExecuteResult(ctx, ts, tool, json.RawMessage(args), false)
	if got == nil {
		t.Fatalf("%s did not ask the gate: %s", tool, result.Text)
	}
	return got.Preview
}

func TestGatePreviewForEditAndWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	write := func(s string) {
		if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx := WithWorkspaceRoots(WithWorkingDir(context.Background(), dir), []WorkspaceRoot{{Path: dir, Access: "write"}})

	write("one\ntwo\nthree\n")
	p := gatePreview(t, ctx, All(), "edit", `{"path":"f.txt","old_string":"two","new_string":"TWO"}`)
	if p == nil || p.Path != path || p.NewFile || p.Truncated || p.Diff != "1   one\n2 - two\n2 + TWO\n3   three" {
		t.Fatalf("edit preview: %+v", p)
	}

	write("one\ntwo\nthree\n")
	p = gatePreview(t, ctx, All(), "write", `{"path":"f.txt","content":"one\nzwei\nthree\n"}`)
	if p == nil || !strings.Contains(p.Diff, "2 - two\n2 + zwei") || p.NewFile {
		t.Fatalf("write preview: %+v", p)
	}

	p = gatePreview(t, ctx, All(), "write", `{"path":"new.txt","content":"a\nb\n"}`)
	if p == nil || !p.NewFile || p.Diff != "1 + a\n2 + b" {
		t.Fatalf("new file preview: %+v", p)
	}

	// LiveAgent Write/Edit: the gate sees the same preview.
	write("alpha\nbeta\n")
	p = gatePreview(t, ctx, LiveAgentCatalog(), "Write", `{"path":"`+filepath.ToSlash(path)+`","content":"alpha\nBETA\n"}`)
	if p == nil || !strings.Contains(p.Diff, "2 - beta\n2 + BETA") {
		t.Fatalf("Write preview: %+v", p)
	}
	write("alpha\nbeta\n")
	cat := LiveAgentCatalog()
	if r := ExecuteResult(ctx, cat, "Read", json.RawMessage(`{"path":"`+filepath.ToSlash(path)+`"}`), false); r.Failed {
		t.Fatal(r.Text)
	}
	p = gatePreview(t, ctx, cat, "Edit", `{"path":"`+filepath.ToSlash(path)+`","old_string":"alpha","new_string":"ALPHA"}`)
	if p == nil || !strings.Contains(p.Diff, "1 - alpha\n1 + ALPHA") {
		t.Fatalf("Edit preview: %+v", p)
	}

	// AI review asks through GateRequestFor; it carries the same preview.
	write("one\ntwo\n")
	req := GateRequestFor(ctx, "edit", json.RawMessage(`{"path":"f.txt","old_string":"one","new_string":"1"}`))
	if req.Preview == nil || !strings.Contains(req.Preview.Diff, "1 - one\n1 + 1") {
		t.Fatalf("GateRequestFor preview: %+v", req.Preview)
	}
}

func TestGatePreviewAbsentOrBounded(t *testing.T) {
	dir := t.TempDir()
	ctx := WithWorkingDir(context.Background(), dir)
	path := filepath.Join(dir, "big.txt")

	if p := gatePreview(t, ctx, All(), "bash", `{"command":"echo hi"}`); p != nil {
		t.Fatalf("bash got a preview: %+v", p)
	}
	if req := GateRequestFor(ctx, "bash", json.RawMessage(`{"command":"echo hi"}`)); req.Preview != nil {
		t.Fatalf("GateRequestFor bash preview: %+v", req.Preview)
	}

	// Many changed lines: cut to the row limit.
	var b strings.Builder
	for i := 0; i < 1000; i++ {
		b.WriteString("line\n")
	}
	p := gatePreview(t, ctx, All(), "write", string(mustJSON(map[string]any{"path": "big.txt", "content": b.String()})))
	if p == nil || !p.Truncated || strings.Count(p.Diff, "\n") > editDiffMaxLines || len(p.Diff) > previewMaxBytes {
		t.Fatalf("row-capped preview: truncated=%v rows=%d bytes=%d", p != nil && p.Truncated, strings.Count(p.Diff, "\n"), len(p.Diff))
	}

	// Long lines: cut to the byte limit at a line boundary.
	long := strings.Repeat("x", 190)
	b.Reset()
	for i := 0; i < editDiffMaxLines; i++ {
		b.WriteString(long + "\n")
	}
	p = gatePreview(t, ctx, All(), "write", string(mustJSON(map[string]any{"path": "wide.txt", "content": b.String()})))
	if p == nil || !p.Truncated || len(p.Diff) > previewMaxBytes || !strings.HasSuffix(p.Diff, " + "+long) {
		t.Fatalf("byte-capped preview: truncated=%v bytes=%d", p != nil && p.Truncated, len(p.Diff))
	}

	// Content past the input limit: no diff, only the truncation mark.
	huge := strings.Repeat("y", previewMaxInput+1)
	p = gatePreview(t, ctx, All(), "write", string(mustJSON(map[string]any{"path": "big.txt", "content": huge})))
	if p == nil || !p.Truncated || p.Diff != "" || p.Path != path {
		t.Fatalf("huge preview: %+v", p)
	}

	// Binary files get no preview.
	bin := filepath.Join(dir, "bin.dat")
	if err := os.WriteFile(bin, []byte{0, 1, 2, 3}, 0o644); err != nil {
		t.Fatal(err)
	}
	if p := gatePreview(t, ctx, All(), "write", `{"path":"bin.dat","content":"text"}`); p != nil {
		t.Fatalf("binary preview: %+v", p)
	}

	// An edit that will fail has nothing to show.
	if err := os.WriteFile(path, []byte("abc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if p := gatePreview(t, ctx, All(), "edit", `{"path":"big.txt","old_string":"missing","new_string":"x"}`); p != nil {
		t.Fatalf("failing edit preview: %+v", p)
	}
}
