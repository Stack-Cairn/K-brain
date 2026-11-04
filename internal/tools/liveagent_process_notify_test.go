package tools

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestManagedProcessExitNotifier(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell commands")
	}
	root := t.TempDir()
	ctx := WithWorkspaceRoots(WithWorkingDir(context.Background(), root), []WorkspaceRoot{{Path: root, Access: "write"}})
	manager := NewManagedProcessManager()
	defer manager.CloseAll()
	notices := make(chan string, 4)
	manager.SetExitNotifier(func(text string) { notices <- text })
	tool := findToolForTest(LiveAgentCatalogWithManagers(nil, NewTerminalManager(), manager), "ManagedProcess")
	run := func(args map[string]any) string {
		t.Helper()
		out, err := tool.Run(ctx, mustJSON(args))
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	// Unobserved exit: the owner is told, with the exit code and the log tail.
	// The process environment also never carries the backend token.
	t.Setenv("K_BRAIN_BACKEND_TOKEN", "bearer-secret-value")
	run(map[string]any{"action": "start", "command": `printf 'built ok%s' "$K_BRAIN_BACKEND_TOKEN"; exit 3`, "label": "build"})
	select {
	case text := <-notices:
		if strings.Contains(text, "bearer-secret-value") {
			t.Fatalf("managed process saw the backend token:\n%s", text)
		}
		for _, want := range []string{"failed exit_code=3", "build", "built ok", "read_log"} {
			if !strings.Contains(text, want) {
				t.Fatalf("notice missing %q:\n%s", want, text)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no exit notice")
	}

	// A wait that watches the exit reports it itself.
	id := fieldValue(run(map[string]any{"action": "start", "command": "sleep 0.2"}), "process_id")
	if out := run(map[string]any{"action": "wait", "process_id": id, "yield_time_ms": 5000}); !strings.Contains(out, "status=completed") {
		t.Fatalf("wait output=%q", out)
	}

	// A process stopped on request is not reported either.
	id = fieldValue(run(map[string]any{"action": "start", "command": "sleep 30"}), "process_id")
	run(map[string]any{"action": "stop", "process_id": id})

	select {
	case text := <-notices:
		t.Fatalf("unexpected notice:\n%s", text)
	case <-time.After(300 * time.Millisecond):
	}
}
