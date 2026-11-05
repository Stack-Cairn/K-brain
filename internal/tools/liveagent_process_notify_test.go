package tools

import (
	"context"
	"runtime"
	"strings"
	"sync"
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

// A yielding Bash call reports a session that ended during its wait, and then no
// exit notice may follow: the notice would arrive as a second report of the same
// exit, after the tool result. Fast commands end before the call's wait starts,
// or right after the wait returned on their first output; both sent one.
func TestYieldingBashReportsEachExitOnce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell commands")
	}
	root := t.TempDir()
	ctx := WithWorkspaceRoots(WithWorkingDir(context.Background(), root), []WorkspaceRoot{{Path: root, Access: "write"}})
	manager := NewManagedProcessManager()
	defer manager.CloseAll()
	var mu sync.Mutex
	notices := map[string]int{}
	manager.SetExitNotifier(func(text string) {
		id, _, _ := strings.Cut(strings.TrimPrefix(text, "[process "), " ")
		mu.Lock()
		notices[id]++
		mu.Unlock()
	})
	// A session that ends before its starting call waits on it: that wait reports
	// the exit, so no notice was due.
	early, err := manager.startBashSession(ctx, "printf ready", root, 0)
	if err != nil {
		t.Fatal(err)
	}
	<-early.done
	time.Sleep(50 * time.Millisecond) // the notice, if any, follows close(done)
	out, err := liveagentProcessWait(ctx, manager, map[string]any{"process_id": early.id, "yield_time_ms": float64(5000)})
	if err != nil {
		t.Fatal(err)
	}
	if status := fieldValue(out, "status"); status != "completed" {
		t.Fatalf("wait on an ended session = %q:\n%s", status, out)
	}
	mu.Lock()
	if n := notices[early.id]; n != 0 {
		t.Errorf("a session that ended before its first wait sent %d exit notices", n)
	}
	mu.Unlock()

	bash := findToolForTest(LiveAgentCatalogWithManagers(nil, NewTerminalManager(), manager), "Bash")
	commands := []string{"true", "printf ready", "printf ready; sleep 0.005", "printf ready; sleep 0.02; printf done"}
	reported := map[string]bool{}
	for i := 0; i < 40; i++ {
		out, err := bash.Run(ctx, mustJSON(map[string]any{"command": commands[i%len(commands)], "yield_time_ms": 5000}))
		if err != nil {
			t.Fatal(err)
		}
		id := fieldValue(out, "process_id")
		status := fieldValue(out, "status")
		if id == "" || status == "" {
			t.Fatalf("Bash output lacks the session state:\n%s", out)
		}
		reported[id] = status != "running"
	}
	// Sessions still running at their result end within a few ms and must notify.
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		missing := 0
		for id, ended := range reported {
			if !ended && notices[id] == 0 {
				missing++
			}
		}
		mu.Unlock()
		if missing == 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	for id, ended := range reported {
		want := 1
		if ended {
			want = 0
		}
		if notices[id] != want {
			t.Errorf("session %s (ended in its result: %v) sent %d exit notices, want %d", id, ended, notices[id], want)
		}
	}
}
