package bashrun

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"
)

func shellTestCommand(posix, windows string) string {
	if runtime.GOOS == "windows" {
		return windows
	}
	return posix
}

func TestNonInteractiveDoesNotHangOnTTYRead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/dev/tty is Unix-specific")
	}
	cmd := `exec 3< /dev/tty; read -r line <&3; echo "got: $line"`
	res := Run(context.Background(), Options{
		Command: cmd,

		Timeout: 5 * time.Second,
	})

	if res.TimedOut {
		t.Fatalf("command hung and timed out — Setsid isolation regressed. output: %q", res.Output)
	}

	if res.Output == "" && res.Exit == "" {
		t.Fatalf("expected a fast non-zero exit; got empty result %+v", res)
	}
}

func TestNonInteractiveCapture(t *testing.T) {
	res := Run(context.Background(), Options{
		Command: shellTestCommand(`echo hi; echo err >&2; exit 3`, `Write-Output hi; [Console]::Error.WriteLine('err'); exit 3`),
	})
	if !strings.Contains(res.Output, "hi") || !strings.Contains(res.Output, "err") {
		t.Fatalf("output missing: %q", res.Output)
	}
	if !strings.Contains(res.Exit, "exit") || !strings.Contains(res.Exit, "3") {
		t.Fatalf("exit status wrong: %q", res.Exit)
	}
	if res.TimedOut {
		t.Fatalf("should not time out: %+v", res)
	}
}

func TestNonInteractiveCleanExit(t *testing.T) {
	res := Run(context.Background(), Options{Command: shellTestCommand(`true`, `$null = 1`)})
	if res.Output != "" || res.Exit != "" {
		t.Fatalf("clean exit should be empty: %+v", res)
	}
}

func TestNonInteractiveTimeout(t *testing.T) {
	res := Run(context.Background(), Options{
		Command: shellTestCommand(`sleep 5`, `Start-Sleep -Seconds 5`),
		Timeout: 100 * time.Millisecond,
	})
	if !res.TimedOut || !res.Killed {
		t.Fatalf("expected Killed+TimedOut: %+v", res)
	}
	if !strings.Contains(res.Exit, "timed out") {
		t.Fatalf("exit text wrong: %q", res.Exit)
	}
}

func TestNonInteractiveCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(80 * time.Millisecond)
		cancel()
	}()
	res := Run(ctx, Options{Command: shellTestCommand(`sleep 5`, `Start-Sleep -Seconds 5`), Timeout: 10 * time.Second})
	if !res.Killed {
		t.Fatalf("cancellation should kill: %+v", res)
	}
}

func TestInteractiveExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native Windows does not provide interactive PTY")
	}
	res := Run(context.Background(), Options{
		Command:     `echo hello; exit 7`,
		Interactive: true,
		Timeout:     5 * time.Second,
		OnOutput:    func(s string) {},
	})
	if !res.Interactive {
		t.Fatalf("expected Interactive result")
	}
	if !strings.Contains(res.Output, "hello") {
		t.Fatalf("interactive output missing: %q", res.Output)
	}

	if res.Exit == "" {
		t.Fatalf("expected non-empty exit status for `exit 7`: %+v", res)
	}
}

func TestInteractiveInactivityTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native Windows does not provide interactive PTY")
	}
	start := time.Now()
	res := Run(context.Background(), Options{
		Command:           `cat`,
		Interactive:       true,
		Timeout:           60 * time.Second,
		InactivityTimeout: 400 * time.Millisecond,
		OnAwaitInput:      func(int) {},
	})
	elapsed := time.Since(start)

	if !res.Killed {
		t.Fatalf("inactivity should kill the command: %+v", res)
	}
	if !strings.Contains(res.Exit, "waiting for input") {
		t.Fatalf("exit text wrong: %q", res.Exit)
	}

	if elapsed > 3*time.Second {
		t.Fatalf("took too long (%s); inactivity timeout not honoured", elapsed)
	}
}

func TestInteractiveKeyForwardingDelaysInactivity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native Windows does not provide interactive PTY")
	}
	keys := make(chan []byte, 16)
	go func() {

		for range 6 {
			time.Sleep(100 * time.Millisecond)
			keys <- []byte("x")
		}

		close(keys)
	}()

	start := time.Now()
	res := Run(context.Background(), Options{
		Command:           `cat`,
		Interactive:       true,
		Timeout:           10 * time.Second,
		InactivityTimeout: 250 * time.Millisecond,
		Keys:              keys,
	})
	elapsed := time.Since(start)

	if !res.Killed {
		t.Fatalf("expected kill after keys stop: %+v", res)
	}

	if elapsed < 550*time.Millisecond {
		t.Fatalf("forwarded keys did not reset the inactivity clock: %s", elapsed)
	}
}

func TestUserShellResolution(t *testing.T) {
	t.Setenv("K_BRAIN_SHELL", "explicit-test-shell")
	if got := userShell(); got != "explicit-test-shell" {
		t.Fatalf("K_BRAIN_SHELL ignored: %q", got)
	}
	t.Setenv("K_BRAIN_SHELL", "")
	if runtime.GOOS == "windows" {
		t.Setenv("SHELL", "/bin/zsh")
		if got := userShell(); got != "pwsh.exe" && got != "powershell.exe" {
			t.Fatalf("Windows default shell: %q", got)
		}
		return
	}
	t.Setenv("SHELL", "/bin/zsh")
	if sh := userShell(); sh != "/bin/zsh" {
		t.Fatalf("$SHELL should win, got %q", sh)
	}

	t.Setenv("SHELL", "")
	if sh := userShell(); sh == "" {
		t.Fatal("empty $SHELL must fall back to the passwd entry or bash")
	}

	t.Setenv("SHELL", "/bin/sh")
	res := Run(context.Background(), Options{Command: "echo shell-ok"})
	if !strings.Contains(res.Output, "shell-ok") || res.Exit != "" {
		t.Fatalf("run via user shell: %+v", res)
	}
}

func TestKeyBytes(t *testing.T) {
	cases := map[string]string{
		"enter":     KeyEnter,
		"esc":       KeyEsc,
		"tab":       KeyTab,
		"backspace": KeyBS,
		"delete":    KeyBS,
		"up":        KeyUp,
		"down":      KeyDown,
		"right":     KeyRight,
		"left":      KeyLeft,
		"bogus":     "",
		"":          "",
	}
	for name, want := range cases {
		if got := KeyBytes(name); got != want {
			t.Errorf("KeyBytes(%q) = %q, want %q", name, got, want)
		}
	}
}
