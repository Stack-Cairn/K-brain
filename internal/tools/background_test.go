package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func backgroundTestCommand(posix, powershell string) string {
	if runtime.GOOS == "windows" {
		return powershell
	}
	return posix
}

// gatedTestCommand prints "first", waits for gate to exist, then prints "second".
func gatedTestCommand(gate string) string {
	return backgroundTestCommand(
		`echo first; while [ ! -f '`+gate+`' ]; do sleep 0.05; done; echo second`,
		`Write-Output first; while (-not (Test-Path '`+gate+`')) { Start-Sleep -Milliseconds 50 }; Write-Output second`,
	)
}

type backgroundHarness struct {
	t        *testing.T
	ctx      context.Context
	fg       *Foreground
	manager  *ManagedProcessManager
	notices  chan string
	mu       sync.Mutex
	changes  []string
	readTool Tool
}

func newBackgroundHarness(t *testing.T) *backgroundHarness {
	t.Helper()
	root := t.TempDir()
	h := &backgroundHarness{t: t, fg: NewForeground(), manager: NewManagedProcessManager(), notices: make(chan string, 4)}
	t.Cleanup(h.manager.CloseAll)
	h.manager.SetExitNotifier(func(text string) { h.notices <- text })
	h.fg.SetOnChange(func(id string, ok bool) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.changes = append(h.changes, id+map[bool]string{true: "+", false: "-"}[ok])
	})
	h.ctx = WithWorkspaceRoots(WithWorkingDir(context.Background(), root), []WorkspaceRoot{{Path: root, Access: "write"}})
	h.readTool = BackgroundProcessTool(h.manager)
	return h
}

type toolOutcome struct {
	out string
	err error
}

// start runs tool as call id and returns once its command is backgroundable and
// has printed "first".
func (h *backgroundHarness) start(ctx context.Context, tool Tool, id string, args map[string]any) <-chan toolOutcome {
	h.t.Helper()
	printed := make(chan struct{})
	var once sync.Once
	ctx = WithOnUpdate(WithToolCallID(ctx, id), func(soFar string) {
		if strings.Contains(soFar, "first") {
			once.Do(func() { close(printed) })
		}
	})
	ctx = WithBackgrounding(ctx, h.fg, func() *ManagedProcessManager { return h.manager })
	done := make(chan toolOutcome, 1)
	go func() {
		out, err := tool.Run(ctx, mustJSON(args))
		done <- toolOutcome{out, err}
	}()
	select {
	case <-printed:
	case res := <-done:
		h.t.Fatalf("tool returned before printing: %+v", res)
	case <-time.After(20 * time.Second):
		h.t.Fatal("command never printed")
	}
	if got := h.fg.Running(); len(got) != 1 || got[0] != id {
		h.t.Fatalf("backgroundable calls = %v", got)
	}
	return done
}

func (h *backgroundHarness) process(action, id string, extra map[string]any) string {
	h.t.Helper()
	args := map[string]any{"action": action, "process_id": id}
	for k, v := range extra {
		args[k] = v
	}
	out, err := h.readTool.Run(h.ctx, mustJSON(args))
	if err != nil {
		h.t.Fatalf("ManagedProcess %s: %v", action, err)
	}
	return out
}

func TestBackgroundForegroundBash(t *testing.T) {
	h := newBackgroundHarness(t)
	gate := filepath.Join(t.TempDir(), "gate")
	callCtx, cancelCall := context.WithCancel(h.ctx)
	defer cancelCall()
	done := h.start(callCtx, bashTool(), "call-1", map[string]any{"command": gatedTestCommand(gate), "timeout": 2})

	processID, err := h.fg.Background(h.ctx, "call-1")
	if err != nil {
		t.Fatal(err)
	}
	var res toolOutcome
	select {
	case res = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("tool call did not return after backgrounding")
	}
	if res.err != nil {
		t.Fatal(res.err)
	}
	for _, want := range []string{"moved this command to the background", "process_id=" + processID, "first", `ManagedProcess(action="wait"`, `ManagedProcess(action="stop"`} {
		if !strings.Contains(res.out, want) {
			t.Fatalf("result missing %q:\n%s", want, res.out)
		}
	}
	if _, soFar, _ := strings.Cut(res.out, "Output so far:"); strings.Contains(soFar, "second\n") {
		t.Fatalf("result has output from after the gate:\n%s", res.out)
	}
	if _, err := h.fg.Background(h.ctx, "call-1"); !errors.Is(err, ErrNotBackgroundable) {
		t.Fatalf("second Background = %v", err)
	}
	if got := h.fg.Running(); len(got) != 0 {
		t.Fatalf("backgroundable calls after backgrounding = %v", got)
	}

	// Cancelling the call (Esc) and the call's own 2s timeout no longer end it.
	cancelCall()
	time.Sleep(2500 * time.Millisecond)
	if out := h.process("status", processID, nil); !strings.Contains(out, "status=running") {
		t.Fatalf("backgrounded command did not survive its call:\n%s", out)
	}

	cursor := fieldValue(res.out, "cursor")
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case notice := <-h.notices:
		for _, want := range []string{processID, "completed", "second"} {
			if !strings.Contains(notice, want) {
				t.Fatalf("exit notice missing %q:\n%s", want, notice)
			}
		}
	case <-time.After(20 * time.Second):
		t.Fatal("no exit notice")
	}
	if out := h.process("wait", processID, map[string]any{"cursor": mustNumber(t, cursor)}); !strings.Contains(out, "second") || strings.Contains(out, "first") {
		t.Fatalf("wait from the result's cursor:\n%s", out)
	}
	if out := h.process("read_log", processID, map[string]any{"cursor": 0}); !strings.Contains(out, "first") || !strings.Contains(out, "second") {
		t.Fatalf("full log:\n%s", out)
	}
	h.mu.Lock()
	changes := strings.Join(h.changes, " ")
	h.mu.Unlock()
	if changes != "call-1+ call-1-" {
		t.Fatalf("backgroundable changes = %q", changes)
	}
}

func TestBackgroundedProcessStops(t *testing.T) {
	h := newBackgroundHarness(t)
	gate := filepath.Join(t.TempDir(), "gate")
	done := h.start(h.ctx, bashTool(), "call-1", map[string]any{"command": gatedTestCommand(gate)})
	processID, err := h.fg.Background(h.ctx, "call-1")
	if err != nil {
		t.Fatal(err)
	}
	<-done
	if out := h.process("stop", processID, nil); !strings.Contains(out, "status=cancelled") {
		t.Fatalf("stop:\n%s", out)
	}
	select {
	case notice := <-h.notices:
		t.Fatalf("a stopped process sent an exit notice:\n%s", notice)
	case <-time.After(300 * time.Millisecond):
	}
	if _, err := h.readTool.Run(h.ctx, mustJSON(map[string]any{"action": "start", "command": "echo no"})); err == nil {
		t.Fatal("the background process tool started a process")
	}
}

func TestStopBackgroundedLeavesOtherProcesses(t *testing.T) {
	h := newBackgroundHarness(t)
	gate := filepath.Join(t.TempDir(), "gate")
	done := h.start(h.ctx, bashTool(), "call-1", map[string]any{"command": gatedTestCommand(gate)})
	processID, err := h.fg.Background(h.ctx, "call-1")
	if err != nil {
		t.Fatal(err)
	}
	<-done
	other, err := h.manager.start(h.ctx, backgroundTestCommand("sleep 30", "Start-Sleep -Seconds 30"), WorkingDir(h.ctx), "", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	h.manager.StopBackgrounded()
	if out := h.process("status", processID, nil); !strings.Contains(out, "status=cancelled") {
		t.Fatalf("backgrounded command survived StopBackgrounded:\n%s", out)
	}
	if !other.snapshot().running() {
		t.Fatal("StopBackgrounded stopped a managed process")
	}
}

func TestBackgroundAfterFinishIsNoop(t *testing.T) {
	h := newBackgroundHarness(t)
	ctx := WithBackgrounding(WithToolCallID(h.ctx, "call-1"), h.fg, func() *ManagedProcessManager { return h.manager })
	out, err := bashTool().Run(ctx, mustJSON(map[string]any{"command": backgroundTestCommand("echo done", "Write-Output done")}))
	if err != nil || !strings.Contains(out, "done") {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if _, err := h.fg.Background(h.ctx, "call-1"); !errors.Is(err, ErrNotBackgroundable) {
		t.Fatalf("Background after finish = %v", err)
	}
	if _, err := h.fg.Background(h.ctx, "unknown"); !errors.Is(err, ErrNotBackgroundable) {
		t.Fatalf("Background of unknown call = %v", err)
	}
}

func TestBackgroundLiveAgentBashUsesCatalogManager(t *testing.T) {
	h := newBackgroundHarness(t)
	catalogManager := NewManagedProcessManager()
	defer catalogManager.CloseAll()
	gate := filepath.Join(t.TempDir(), "gate")
	catalog := LiveAgentCatalogWithManagers(nil, NewTerminalManager(), catalogManager)
	done := h.start(h.ctx, findToolForTest(catalog, "Bash"), "call-1", map[string]any{"command": gatedTestCommand(gate)})
	processID, err := h.fg.Background(h.ctx, "call-1")
	if err != nil {
		t.Fatal(err)
	}
	if res := <-done; res.err != nil || !strings.Contains(res.out, processID) {
		t.Fatalf("out=%q err=%v", res.out, res.err)
	}
	if _, err := catalogManager.get(processID); err != nil {
		t.Fatalf("backgrounded Bash is not in the catalog's manager: %v", err)
	}
	if _, err := h.manager.get(processID); err == nil {
		t.Fatal("backgrounded Bash landed in the context's manager")
	}
	out, err := findToolForTest(catalog, "ManagedProcess").Run(h.ctx, mustJSON(map[string]any{"action": "stop", "process_id": processID}))
	if err != nil || !strings.Contains(out, "status=cancelled") {
		t.Fatalf("stop: out=%q err=%v", out, err)
	}
}

func mustNumber(t *testing.T, s string) float64 {
	t.Helper()
	n, err := strconv.ParseFloat(s, 64)
	if err != nil {
		t.Fatal(err)
	}
	return n
}
