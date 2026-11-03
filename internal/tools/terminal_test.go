package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// These cases use Unix shell commands; ConPTY has separate Windows tests.
func requireUnixPTY(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell commands")
	}
}

func TestTerminalManagerReadRequiresOwningCanonicalRun(t *testing.T) {
	requireUnixPTY(t)
	manager := NewTerminalManager()
	ctx := WithRunIdentity(WithWorkingDir(context.Background(), t.TempDir()), RunIdentity{ConversationID: "session-1", RunID: "run-1"})
	id, err := manager.Start(ctx, "printf terminal-output", WorkingDir(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.CloseAll()
	var output string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		output, err = manager.Read(ctx, id, 1024)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(output, "terminal-output") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(output, "terminal-output") {
		t.Fatalf("output=%q", output)
	}
	later := WithRunIdentity(context.Background(), RunIdentity{ConversationID: "session-1", RunID: "run-2"})
	if output, err := manager.Read(later, id, 1024); err != nil || !strings.Contains(output, "terminal-output") {
		t.Fatalf("later run of the same conversation read=%q error=%v", output, err)
	}
	wrong := WithRunIdentity(context.Background(), RunIdentity{ConversationID: "session-2", RunID: "run-1"})
	if _, err := manager.Read(wrong, id, 1024); err == nil || !strings.Contains(err.Error(), "another conversation") {
		t.Fatalf("wrong owner read error=%v", err)
	}
}

func TestTerminalProtocolInteractiveLifecycle(t *testing.T) {
	requireUnixPTY(t)
	manager := NewTerminalManager()
	defer manager.CloseAll()
	ctx, cancel := context.WithCancel(WithRunIdentity(WithWorkingDir(context.Background(), t.TempDir()), RunIdentity{ConversationID: "conversation", RunID: "run"}))
	defer cancel()
	req := TerminalRequest{Action: "create", ConversationID: "conversation", RunID: "run", Cols: 100, Rows: 30}
	out, err := manager.Handle(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if out.Session.Cols != 100 || out.Session.Rows != 30 || !out.Session.Running {
		t.Fatalf("record=%+v", out.Session)
	}
	req.SessionID = out.Session.ID
	req.Action = "input"
	req.Data = "printf '\164erminal-protocol-marker\n'\n"
	if _, err := manager.Handle(ctx, req); err != nil {
		t.Fatal(err)
	}
	req.Action = "read"
	req.MaxBytes = 4096
	for deadline := time.Now().Add(3 * time.Second); ; {
		out, err = manager.Handle(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(out.Output), "terminal-protocol-marker") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("output=%q", out.Output)
		}
		time.Sleep(10 * time.Millisecond)
	}
	req.Action = "resize"
	req.Cols = 120
	req.Rows = 40
	out, err = manager.Handle(ctx, req)
	if err != nil || out.Session.Cols != 120 {
		t.Fatalf("resize=%+v %v", out, err)
	}
	req.Action = "list"
	out, err = manager.Handle(ctx, req)
	if err != nil || len(out.Sessions) != 1 {
		t.Fatalf("list=%+v %v", out, err)
	}
	req.Action = "close"
	out, err = manager.Handle(ctx, req)
	if err != nil || out.Session.Running {
		t.Fatalf("close=%+v %v", out, err)
	}
	req.Action = "read"
	out, err = manager.Handle(ctx, req)
	if err != nil || len(out.Output) == 0 {
		t.Fatalf("retained output=%+v %v", out, err)
	}
}

func TestTerminalBoundedBufferAndOwnerValidationBeforeSpawn(t *testing.T) {
	requireUnixPTY(t)
	manager := NewTerminalManager()
	defer manager.CloseAll()
	root := t.TempDir()
	if _, err := manager.Start(WithWorkingDir(context.Background(), root), "touch should-not-exist", root); err == nil {
		t.Fatal("unowned spawn accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "should-not-exist")); !os.IsNotExist(err) {
		t.Fatalf("command executed: %v", err)
	}
	ctx := WithRunIdentity(WithWorkingDir(context.Background(), root), RunIdentity{ConversationID: "conversation", RunID: "run"})
	id, err := manager.Start(ctx, "head -c 600000 /dev/zero", root)
	if err != nil {
		t.Fatal(err)
	}
	session, err := manager.ownedSession(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-session.done:
	case <-time.After(3 * time.Second):
		t.Fatal("child did not finish")
	}
	out, err := manager.snapshot(ctx, id, terminalMaxTail)
	if err != nil || len(out.Output) != terminalMaxTail || !out.Truncated || out.OutputEndOffset != 600000 || out.OutputStartOffset != 600000-terminalMaxTail {
		t.Fatalf("len=%d offsets=%d..%d truncated=%t err=%v", len(out.Output), out.OutputStartOffset, out.OutputEndOffset, out.Truncated, err)
	}
	manager.CloseAll()
	if _, err := manager.Start(ctx, "printf late", root); err == nil {
		t.Fatal("closed registry accepted child")
	}
}
