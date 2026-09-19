package bashrun

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRunReportsUnstartableShell(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		res := Run(context.Background(), Options{
			Shell:             "definitely-not-a-k-brain-shell",
			Command:           "echo hi",
			Interactive:       interactive,
			Timeout:           5 * time.Second,
			InactivityTimeout: time.Second,
		})
		if res.Exit == "" {
			t.Fatalf("interactive=%v: a shell that can't start must report an exit status: %+v", interactive, res)
		}

		if runtime.GOOS == "windows" && interactive {
			if !strings.Contains(res.Exit, "interactive PTY is not available") {
				t.Fatalf("Windows must report unsupported PTY: %+v", res)
			}
		} else if !strings.Contains(res.Exit, "exit:") {
			t.Fatalf("interactive=%v: exit should name the start failure, got %q", interactive, res.Exit)
		}
		if res.Output != "" {
			t.Fatalf("interactive=%v: nothing ran, so there is no output: %q", interactive, res.Output)
		}
	}
}

func TestInteractiveNeverOutlivesItsCaps(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native Windows does not provide interactive PTY")
	}
	cases := map[string]struct {
		setup    func() (context.Context, time.Duration)
		wantExit string
		wantTO   bool
	}{
		"hard timeout": {
			setup: func() (context.Context, time.Duration) {
				return context.Background(), 200 * time.Millisecond
			},
			wantExit: "timed out",
			wantTO:   true,
		},
		"cancellation": {
			setup: func() (context.Context, time.Duration) {
				ctx, cancel := context.WithCancel(context.Background())
				time.AfterFunc(150*time.Millisecond, cancel)
				t.Cleanup(cancel)
				return ctx, 30 * time.Second
			},
			wantExit: "cancelled",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, timeout := tc.setup()
			start := time.Now()
			res := Run(ctx, Options{
				Command:           "sleep 30",
				Interactive:       true,
				Timeout:           timeout,
				InactivityTimeout: 500 * time.Millisecond,
			})
			elapsed := time.Since(start)
			if !res.Killed || !res.Interactive {
				t.Fatalf("expected a killed interactive result: %+v", res)
			}
			if res.Exit != tc.wantExit {
				t.Fatalf("exit reason: got %q, want %q (%+v)", res.Exit, tc.wantExit, res)
			}
			if res.TimedOut != tc.wantTO {
				t.Fatalf("TimedOut: got %v, want %v (%+v)", res.TimedOut, tc.wantTO, res)
			}
			if elapsed > 10*time.Second {
				t.Fatalf("the child outlived its caps by %s", elapsed)
			}
		})
	}
}

func TestExitString(t *testing.T) {
	if got := exitString(nil); got != "" {
		t.Fatalf("a clean exit must render empty, got %q", got)
	}
	if got := exitString(errors.New("fork/exec: no such file")); got != "(exit: fork/exec: no such file)" {
		t.Fatalf("non-exit errors: %q", got)
	}
	if runtime.GOOS == "windows" {
		err := exec.CommandContext(t.Context(), "cmd.exe", "/C", "exit 5").Run()
		if got := exitString(err); got != "(exit: exit status 5)" {
			t.Fatalf("exit status: %q", got)
		}
		return
	}
	err := exec.CommandContext(t.Context(), "sh", "-c", "exit 5").Run()
	if got := exitString(err); got != "(exit: exit status 5)" {
		t.Fatalf("exit status: %q", got)
	}
}

func TestIsKilledBySignal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix signals are not available on Windows")
	}
	if isKilledBySignal(errors.New("boom")) {
		t.Fatal("a non-exec error is not a signal kill")
	}
	if isKilledBySignal(exec.CommandContext(t.Context(), "sh", "-c", "exit 2").Run()) {
		t.Fatal("a plain non-zero exit is not a signal kill")
	}
	err := exec.CommandContext(t.Context(), "sh", "-c", "kill -9 $$").Run()
	if err == nil {
		t.Fatal("self-SIGKILL should fail the command")
	}
	if !isKilledBySignal(err) {
		t.Fatalf("SIGKILL should be reported as a signal kill: %v", err)
	}
}

func TestTrackIgnoresUnstartedCommands(t *testing.T) {
	before := trackedCount()
	unstarted := exec.CommandContext(t.Context(), "true")
	track(unstarted)
	if got := trackedCount(); got != before {
		t.Fatalf("tracking an unstarted command changed the registry: %d -> %d", before, got)
	}
	untrack(unstarted)
	if got := trackedCount(); got != before {
		t.Fatalf("untracking an unstarted command changed the registry: %d -> %d", before, got)
	}
}

func TestKillAllSkipsProcesslessEntries(t *testing.T) {
	trackMu.Lock()
	tracked[-1] = &trackedProcess{cmd: exec.CommandContext(t.Context(), "true")}
	trackMu.Unlock()
	t.Cleanup(func() {
		trackMu.Lock()
		delete(tracked, -1)
		trackMu.Unlock()
	})

	KillAll()

	trackMu.Lock()
	_, still := tracked[-1]
	trackMu.Unlock()
	if !still {
		t.Fatal("KillAll should not mutate the registry")
	}
}
