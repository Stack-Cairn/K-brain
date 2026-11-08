package bashrun

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/process"
)

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// gatedCommand prints prefix, waits for the gate file, then prints "second".
func gatedCommand(prefix string) string {
	return shellTestCommand(
		prefix+`; while [ ! -f "$K_BRAIN_TEST_GATE" ]; do sleep 0.05; done; echo second`,
		prefix+`; while (-not (Test-Path $env:K_BRAIN_TEST_GATE)) { Start-Sleep -Milliseconds 50 }; Write-Output second`,
	)
}

func startDetachable(t *testing.T, ctx context.Context, command string, timeout time.Duration) (gate string, detach chan struct{}, seen chan string, done chan Result) {
	t.Helper()
	gate = filepath.Join(t.TempDir(), "gate")
	detach = make(chan struct{})
	seen = make(chan string, 64)
	done = make(chan Result, 1)
	go func() {
		done <- Run(ctx, Options{
			Command: command,
			Timeout: timeout,
			Env:     []string{"K_BRAIN_TEST_GATE=" + gate},
			Detach:  detach,
			OnUpdate: func(tail string) {
				select {
				case seen <- tail:
				default:
				}
			},
		})
	}()
	return gate, detach, seen, done
}

func waitForOutput(t *testing.T, seen <-chan string, want string, done <-chan Result) {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for {
		select {
		case tail := <-seen:
			if strings.Contains(tail, want) {
				return
			}
		case res := <-done:
			t.Fatalf("command ended before printing %q: %+v", want, res)
		case <-deadline:
			t.Fatalf("command never printed %q", want)
		}
	}
}

func TestDetachHandsOverRunningCommand(t *testing.T) {
	before := trackedCount()
	ctx, cancel := context.WithCancel(t.Context())
	gate, detach, seen, done := startDetachable(t, ctx, gatedCommand(shellTestCommand("echo first", "Write-Output first")), 2*time.Second)
	waitForOutput(t, seen, "first", done)
	close(detach)
	res := <-done
	d := res.Detached
	if d == nil {
		t.Fatalf("command was not detached: %+v", res)
	}
	if out, total := d.Output(); !strings.Contains(out, "first") || total == 0 {
		t.Fatalf("output so far = %q (%d bytes)", out, total)
	}

	// Neither cancelling the call's context nor its timeout ends the command.
	cancel()
	time.Sleep(2500 * time.Millisecond)
	if !process.Alive(d.Cmd) {
		t.Fatal("detached command died with its call")
	}
	if trackedCount() != before+1 {
		t.Fatal("detached command is no longer tracked")
	}

	var log lockedBuffer
	if err := d.Stream(&log); err != nil {
		t.Fatal(err)
	}
	if err := d.Stream(&log); err == nil {
		t.Fatal("second Stream succeeded")
	}
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := d.Wait(); err != nil {
		t.Fatalf("detached command failed: %v", err)
	}
	if got := log.String(); !strings.Contains(got, "first") || !strings.Contains(got, "second") || strings.Index(got, "first") > strings.Index(got, "second") {
		t.Fatalf("streamed output = %q", got)
	}
	if trackedCount() != before {
		t.Fatal("finished detached command retained in registry")
	}
}

func TestDetachKeepsSpilledOutput(t *testing.T) {
	big := shellTestCommand(`head -c 1500000 /dev/zero | tr '\0' a; echo; echo ready`, `[Console]::Out.Write('a' * 1500000); Write-Output ''; Write-Output ready`)
	_, detach, seen, done := startDetachable(t, t.Context(), gatedCommand(big), time.Minute)
	waitForOutput(t, seen, "ready", done)
	close(detach)
	res := <-done
	d := res.Detached
	if d == nil {
		t.Fatalf("command was not detached: %+v", res)
	}
	defer func() { d.Kill(); _ = d.Wait() }()
	_, total := d.Output()
	var log lockedBuffer
	if err := d.Stream(&log); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(log.String(), "a"); int64(len(log.String())) != total || got < 1500000 || strings.Contains(log.String(), "not kept") {
		t.Fatalf("streamed %d bytes (%d a's), printed %d", len(log.String()), got, total)
	}
}

func TestDetachedKill(t *testing.T) {
	before := trackedCount()
	_, detach, seen, done := startDetachable(t, t.Context(), gatedCommand(shellTestCommand("echo first", "Write-Output first")), time.Minute)
	waitForOutput(t, seen, "first", done)
	close(detach)
	d := (<-done).Detached
	if d == nil {
		t.Fatal("command was not detached")
	}
	d.Kill()
	finished := make(chan error, 1)
	go func() { finished <- d.Wait() }()
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("killed command reported success")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("killed command did not exit")
	}
	if trackedCount() != before {
		t.Fatal("killed detached command retained in registry")
	}
}

func TestDetachWithoutRequestRunsNormally(t *testing.T) {
	res := Run(t.Context(), Options{Command: shellTestCommand("echo hi", "Write-Output hi"), Detach: make(chan struct{})})
	if res.Detached != nil || !strings.Contains(res.Output, "hi") || res.Exit != "" {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestDetachedCommandStillTimesOutBeforeDetach(t *testing.T) {
	res := Run(t.Context(), Options{Command: shellTestCommand("sleep 30", "Start-Sleep -Seconds 30"), Timeout: 300 * time.Millisecond, Detach: make(chan struct{})})
	if !res.TimedOut || res.Detached != nil {
		t.Fatalf("expected a timeout: %+v", res)
	}
}
