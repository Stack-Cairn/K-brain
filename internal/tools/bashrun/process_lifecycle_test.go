package bashrun

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestBashrunProcessHelper(t *testing.T) {
	mode := os.Getenv("K_BRAIN_TEST_PROCESS_HELPER")
	if mode == "" {
		return
	}
	if mode == "sleep" {
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	code, err := strconv.Atoi(mode)
	if err != nil {
		os.Exit(99)
	}
	os.Exit(code)
}

func lifecycleCommand(t *testing.T, ctx context.Context, mode string) *exec.Cmd {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, path, "-test.run=^TestBashrunProcessHelper$")
	cmd.Env = append(os.Environ(), "K_BRAIN_TEST_PROCESS_HELPER="+mode)
	return cmd
}

func TestRunPipedNaturalExitIsNotKilled(t *testing.T) {
	for _, mode := range []string{"0", "7"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			before := trackedCount()
			res := runPiped(ctx, lifecycleCommand(t, ctx, mode), nil)
			if res.Killed || res.TimedOut || (res.Exit == "") != (mode == "0") {
				t.Fatalf("natural exit misclassified: %+v", res)
			}
			if trackedCount() != before {
				t.Fatal("completed process retained in registry")
			}
		})
	}
}

func TestConcurrentKillAll(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	cmd := lifecycleCommand(t, ctx, "sleep")
	done := make(chan Result, 1)
	go func() { done <- runPiped(ctx, cmd, nil) }()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		trackMu.Lock()
		found := false
		for _, p := range tracked {
			found = found || p.cmd == cmd
		}
		trackMu.Unlock()
		if found {
			break
		}
		select {
		case res := <-done:
			t.Fatalf("process exited before registration: %+v", res)
		case <-deadline.C:
			t.Fatal("process never registered")
		case <-time.After(5 * time.Millisecond):
		}
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(KillAll)
	}
	wg.Wait()
	select {
	case res := <-done:
		if !res.Killed || res.TimedOut || res.Exit == "" {
			t.Fatalf("explicit termination misclassified: %+v", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not finish after concurrent KillAll")
	}
	if trackedCount() != 0 {
		t.Fatal("killed process retained in registry")
	}
}

func TestTrackedProcessFinishedAndFailedKill(t *testing.T) {
	p := &trackedProcess{cmd: &exec.Cmd{}}
	p.kill()
	if p.finish(errors.New("ordinary failure")) {
		t.Fatal("failed kill classified as termination")
	}
	p.cmd = nil
	p.kill()
	p.killed = true
	if p.finish(nil) {
		t.Fatal("successful natural exit classified as termination")
	}
}

func TestUntrackPreservesReplacement(t *testing.T) {
	old := &exec.Cmd{Process: &os.Process{Pid: -2}}
	replacement := &exec.Cmd{Process: &os.Process{Pid: -2}}
	track(old)
	p := track(replacement)
	defer untrack(replacement)
	untrack(old)
	trackMu.Lock()
	got := tracked[-2]
	trackMu.Unlock()
	if got != p {
		t.Fatal("untracking an old process removed its replacement")
	}
}
