package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/sandbox"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func waitTestShell(command, windowsCommand string) (string, string) {
	if runtime.GOOS == "windows" {
		return "powershell", windowsCommand
	}
	return "bash", command
}

func awaitWaitTask(t *testing.T, w *waitTask, want WaitStatus) {
	t.Helper()
	select {
	case <-w.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("wait task did not settle")
	}
	if got := w.Status(); got != want {
		t.Fatalf("status = %s, want %s: %s", got, want, w.Detail)
	}
}

func TestWaitTimeoutInterruptsCommand(t *testing.T) {
	ag := New(ai.New("http://unused", "k"), "m", 100, "sys")
	defer ag.Waits().Close()
	shell, command := waitTestShell("sleep 30", "Start-Sleep -Seconds 30")
	w, err := ag.StartWait(WaitTaskSpec{Shell: shell, Command: command, Timeout: 700 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	awaitWaitTask(t, w, WaitTimeout)
}

func TestWaitCloseSettlesTasks(t *testing.T) {
	ag := New(ai.New("http://unused", "k"), "m", 100, "sys")
	reg := ag.Waits()
	defer reg.Close()
	var woke atomic.Int32
	reg.OnWake = func(string) { woke.Add(1) }
	shell, command := waitTestShell("sleep 30", "Start-Sleep -Seconds 30")
	var tasks []*waitTask
	for range 3 {
		w, err := ag.StartWait(WaitTaskSpec{Shell: shell, Command: command, Timeout: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, w)
	}
	reg.Close()
	reg.Close()
	for _, w := range tasks {
		awaitWaitTask(t, w, WaitKilled)
	}
	reg.mu.Lock()
	left := len(reg.waits)
	reg.mu.Unlock()
	if left != 0 || woke.Load() != 0 {
		t.Fatalf("close leaked tasks or notifications: tasks=%d notifications=%d", left, woke.Load())
	}
	if _, err := ag.StartWait(WaitTaskSpec{Command: "exit 0"}); err == nil {
		t.Fatal("closed registry accepted a new task")
	}
}

func TestWaitInheritsCallerCancellation(t *testing.T) {
	ag := New(ai.New("http://unused", "k"), "m", 100, "sys")
	defer ag.Waits().Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	shell, command := waitTestShell("sleep 30", "Start-Sleep -Seconds 30")
	w, err := ag.StartWaitContext(ctx, WaitTaskSpec{Shell: shell, Command: command})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	awaitWaitTask(t, w, WaitKilled)
}

func TestWaitInheritsWorkingDirectory(t *testing.T) {
	ag := New(ai.New("http://unused", "k"), "m", 100, "sys")
	defer ag.Waits().Close()
	ag.WorkingDir = t.TempDir()
	if err := os.WriteFile(filepath.Join(ag.WorkingDir, "ready.txt"), []byte("ready-from-worktree"), 0o600); err != nil {
		t.Fatal(err)
	}
	shell, command := waitTestShell("cat ready.txt", "Get-Content -LiteralPath ready.txt")
	w, err := ag.StartWait(WaitTaskSpec{Shell: shell, Command: command, Until: "ready-from-worktree"})
	if err != nil {
		t.Fatal(err)
	}
	awaitWaitTask(t, w, WaitMet)
}

func TestWaitToolUsesExplicitShell(t *testing.T) {
	t.Setenv("K_BRAIN_SHELL", "missing-shell-for-wait-test")
	ag := New(ai.New("http://unused", "k"), "m", 100, "sys")
	defer ag.Waits().Close()
	woke := make(chan string, 1)
	ag.Waits().OnWake = func(text string) { woke <- text }
	shell, command := waitTestShell("echo explicit-shell-ok", "Write-Output explicit-shell-ok")
	args, _ := json.Marshal(map[string]any{"shell": shell, "command": command, "until": "explicit-shell-ok", "timeout": 5})
	if _, err := waitTool(ag).Run(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	select {
	case msg := <-woke:
		if !strings.Contains(msg, "condition met") {
			t.Fatalf("explicit shell not used: %s", msg)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("wait tool did not deliver notification")
	}
}

func TestWaitDefaultInterval(t *testing.T) {
	ag := New(ai.New("http://unused", "k"), "m", 100, "sys")
	defer ag.Waits().Close()
	w, err := ag.StartWait(WaitTaskSpec{Command: "exit 0"})
	if err != nil {
		t.Fatal(err)
	}
	if w.Interval != 10*time.Second {
		t.Fatalf("default interval = %s", w.Interval)
	}
}

func TestWaitHonorsPermissionAndSandboxContext(t *testing.T) {
	ag := New(ai.New("http://unused", "k"), "m", 100, "sys")
	ag.WorkingDir = t.TempDir()
	ag.SandboxPolicy = sandbox.New("strict", "auto", ag.WorkingDir, false, nil, nil)
	old := tools.Gate
	t.Cleanup(func() { tools.Gate = old })
	var calls int
	tools.Gate = func(req tools.GateRequest) (tools.GateDecision, string) {
		calls++
		if req.Tool != "bash" || req.Command != "exit 0" {
			t.Errorf("unexpected permission request: %+v", req)
		}
		if sandbox.FromContext(req.Context) != ag.SandboxPolicy || tools.WorkingDir(req.Context) != ag.WorkingDir {
			t.Error("wait permission request lost execution context")
		}
		return tools.GateReject, "test rejection"
	}
	if _, err := ag.StartWait(WaitTaskSpec{Command: "exit 0"}); err == nil || !strings.Contains(err.Error(), "test rejection") {
		t.Fatalf("rejected command: %v", err)
	}
	if calls != 1 {
		t.Fatalf("permission calls = %d", calls)
	}
	ag.SetPlanMode(true)
	if _, err := ag.StartWait(WaitTaskSpec{Command: "exit 0"}); err == nil {
		t.Fatal("plan mode allowed background command")
	}
	if calls != 1 {
		t.Fatal("plan mode should reject before requesting permission")
	}
}

func TestWaitConditionMetImmediately(t *testing.T) {
	ag := New(ai.New("http://unused", "k"), "m", 100, "sys")
	defer ag.Waits().Close()

	var woke atomic.Int32
	ag.Waits().OnWake = func(string) { woke.Add(1) }

	w, err := ag.StartWait(WaitTaskSpec{Command: "exit 0", Interval: 50 * time.Millisecond, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.Done:
	case <-time.After(2 * time.Second):
		t.Fatal("wait never delivered")
	}
	if w.Status() != WaitMet {
		t.Fatalf("status = %q, want %q", w.Status(), WaitMet)
	}
	if !strings.Contains(w.Detail, "condition met") {
		t.Fatalf("detail = %q", w.Detail)
	}
	if got := woke.Load(); got != 1 {
		t.Fatalf("idle wake fired %d times, want exactly 1", got)
	}
}

func TestWaitUntilRegex(t *testing.T) {
	ag := New(ai.New("http://unused", "k"), "m", 100, "sys")
	defer ag.Waits().Close()
	ag.Waits().OnWake = func(string) {}

	w, err := ag.StartWait(WaitTaskSpec{Command: "echo running", Until: "ready", Interval: 50 * time.Millisecond, Timeout: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	<-w.Done
	if w.Status() != WaitTimeout {
		t.Fatalf("status = %q, want %q (until never matched)", w.Status(), WaitTimeout)
	}

	if _, err := ag.StartWait(WaitTaskSpec{Command: "true", Until: "[unclosed"}); err == nil {
		t.Fatal("bad until regex should fail StartWait")
	}
}

func TestWaitStrikesOut(t *testing.T) {
	ag := New(ai.New("http://unused", "k"), "m", 100, "sys")
	defer ag.Waits().Close()
	ag.Waits().OnWake = func(string) {}

	start := time.Now()
	w, err := ag.StartWait(WaitTaskSpec{Command: "exit 1", Interval: 30 * time.Millisecond, Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	<-w.Done
	if w.Status() != WaitFailed {
		t.Fatalf("status = %q, want %q", w.Status(), WaitFailed)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("strike-out should beat the timeout: took %s", d)
	}
}

func TestWaitTimeout(t *testing.T) {
	ag := New(ai.New("http://unused", "k"), "m", 100, "sys")
	defer ag.Waits().Close()
	ag.Waits().OnWake = func(string) {}

	w, err := ag.StartWait(WaitTaskSpec{Command: "echo still-waiting", Until: "never-matches", Interval: 30 * time.Millisecond, Timeout: 150 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	<-w.Done
	if w.Status() != WaitTimeout {
		t.Fatalf("status = %q, want %q", w.Status(), WaitTimeout)
	}
	if !strings.Contains(w.Detail, "timeout") {
		t.Fatalf("detail = %q", w.Detail)
	}
}

func TestWaitBusySteersInsteadOfWaking(t *testing.T) {
	ag := New(ai.New("http://unused", "k"), "m", 100, "sys")
	defer ag.Waits().Close()
	ag.running.Store(true)

	var woke atomic.Int32
	ag.Waits().OnWake = func(string) { woke.Add(1) }

	w, err := ag.StartWait(WaitTaskSpec{Command: "exit 0", Interval: 30 * time.Millisecond, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	<-w.Done
	if woke.Load() != 0 {
		t.Fatal("busy delivery must not fire OnWake")
	}
	if got := len(ag.drainPending()); got != 1 {
		t.Fatalf("busy delivery should queue exactly one steer, got %d", got)
	}
}

func TestWaitCancel(t *testing.T) {
	ag := New(ai.New("http://unused", "k"), "m", 100, "sys")
	defer ag.Waits().Close()
	ag.Waits().OnWake = func(string) {}

	w, err := ag.StartWait(WaitTaskSpec{Command: "echo x", Until: "never", Interval: time.Second, Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if !ag.Waits().CancelWait(w.ID) {
		t.Fatal("cancel of a running wait should succeed")
	}
	if w.Status() != WaitKilled {
		t.Fatalf("status = %q, want %q", w.Status(), WaitKilled)
	}
	if ag.Waits().CancelWait(w.ID) {
		t.Fatal("second cancel should report not-running")
	}
}

func TestWaitToolRegisters(t *testing.T) {
	ag := New(ai.New("http://unused", "k"), "m", 100, "sys")
	defer ag.Waits().Close()

	var wt tools.Tool
	for _, tl := range ag.AllTools() {
		if tl.Def.Function.Name == "wait" {
			wt = tl
		}
	}
	if wt.Def.Function.Name == "" {
		t.Fatal("agent should expose the wait tool")
	}
	out, err := wt.Run(t.Context(), json.RawMessage(`{"command":"exit 0","interval":0.05,"timeout":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "do NOT sleep-poll") {
		t.Fatalf("tool output should state the no-poll contract: %q", out)
	}

	ag.Waits().mu.Lock()
	var ws []*waitTask
	for _, w := range ag.Waits().waits {
		ws = append(ws, w)
	}
	ag.Waits().mu.Unlock()
	if len(ws) > 1 {
		t.Fatalf("at most one wait should be registered, got %d", len(ws))
	}
	if len(ws) == 1 {
		select {
		case <-ws[0].Done:
		case <-time.After(2 * time.Second):
			t.Fatal("registered wait never settled")
		}

		ag.Waits().mu.Lock()
		left := len(ag.Waits().waits)
		ag.Waits().mu.Unlock()
		if left != 0 {
			t.Fatalf("settled wait should be deleted from the registry, %d left", left)
		}
	}
}

func TestWaitTickerPath(t *testing.T) {
	ag := New(ai.New("http://unused", "k"), "m", 100, "sys")
	defer ag.Waits().Close()
	ag.Waits().OnWake = func(string) {}

	ag.WorkingDir = t.TempDir()
	flag := filepath.Join(ag.WorkingDir, "ready")
	polled := filepath.Join(ag.WorkingDir, "polled")
	shell, command := waitTestShell(
		"if [ -f ready ]; then cat ready; else printf checked > polled; fi",
		"if (Test-Path -LiteralPath ready) { Get-Content -LiteralPath ready } else { Set-Content -LiteralPath polled -Value checked }")
	w, err := ag.StartWait(WaitTaskSpec{
		Command:  command,
		Shell:    shell,
		Until:    "READY",
		Interval: waitMinInterval,
		Timeout:  30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(polled); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first poll did not run")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := os.WriteFile(flag, []byte("READY"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.Done:
	case <-time.After(10 * time.Second):
		t.Fatal("ticker-path wait never settled")
	}
	if w.Status() != WaitMet {
		t.Fatalf("status = %q, want %q", w.Status(), WaitMet)
	}
}

func TestTurnTeardownDrainsOrphanedSteers(t *testing.T) {
	ag := New(ai.New("http://unused", "k"), "m", 100, "sys")
	var woke []string
	ag.OnOrphanedSteer = func(s string) { woke = append(woke, s) }

	ag.running.Store(true)
	ag.Steer("orphaned message")

	ag.running.Store(false)
	ag.drainOrphanedSteers()

	if len(woke) != 1 || woke[0] != "orphaned message" {
		t.Fatalf("orphaned steer should wake, got %v", woke)
	}
	if len(ag.drainPending()) != 0 {
		t.Fatal("pending should be empty after teardown drain")
	}
}

func TestWaitCancelRacesDeliver(t *testing.T) {
	for range 200 {
		ag := New(ai.New("http://unused", "k"), "m", 100, "sys")
		ag.Waits().OnWake = func(string) {}
		w, err := ag.StartWait(WaitTaskSpec{Command: "exit 0", Interval: waitMinInterval, Timeout: time.Minute})
		if err != nil {
			t.Fatal(err)
		}

		go ag.Waits().CancelWait(w.ID)
		<-w.Done
		ag.Waits().Close()
	}
}
