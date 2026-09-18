package bashrun

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/process"
)

func TestWindowsShellExecution(t *testing.T) {
	for _, shell := range []string{"powershell", "pwsh", "powershell7", "cmd", "bash", "wsl"} {
		t.Run(shell, func(t *testing.T) {
			cmd, err := shellCommand(t.Context(), shell, "echo test")
			if err != nil {
				t.Fatal(err)
			}
			if cmd.Err != nil {
				t.Skipf("shell not installed: %v", cmd.Err)
			}
			if shell == "wsl" && os.Getenv("K_BRAIN_TEST_WSL") != "1" {
				t.Skip("set K_BRAIN_TEST_WSL=1 to exercise an installed WSL distribution")
			}
			command := "echo native-shell-ok"
			if shell == "powershell" || shell == "pwsh" || shell == "powershell7" {
				command = "Write-Output 'native-shell-ok 中文'; [Console]::Error.WriteLine('stderr-ok'); exit 7"
			}
			res := Run(t.Context(), Options{Shell: shell, Command: command, Timeout: 30 * time.Second})
			if !strings.Contains(res.Output, "native-shell-ok") || res.TimedOut {
				t.Fatalf("%+v", res)
			}
			if strings.HasPrefix(command, "Write-Output") {
				if !strings.Contains(res.Output, "中文") || !strings.Contains(res.Output, "stderr-ok") || !strings.Contains(res.Exit, "7") {
					t.Fatalf("Unicode/stderr/exit code lost: %+v", res)
				}
			} else if res.Exit != "" {
				t.Fatalf("%+v", res)
			}
		})
	}
}

func TestWindowsShellDefaultsAndContext(t *testing.T) {
	t.Setenv("K_BRAIN_SHELL", "")
	t.Setenv("SHELL", "/bin/bash")
	if got := DefaultShell(); got != "pwsh.exe" && got != "powershell.exe" {
		t.Fatal(got)
	}
	t.Setenv("K_BRAIN_SHELL", "missing-shell")
	res := Run(WithShell(t.Context(), "powershell"), Options{Command: "Write-Output context-ok"})
	if res.Exit != "" || !strings.Contains(res.Output, "context-ok") {
		t.Fatalf("%+v", res)
	}
	res = Run(WithShell(t.Context(), "missing-shell"), Options{Shell: "powershell", Command: "Write-Output option-ok"})
	if res.Exit != "" || !strings.Contains(res.Output, "option-ok") {
		t.Fatalf("%+v", res)
	}
}

func TestWindowsPowerShellErrorsAndCwd(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, command := range []string{"throw 'broken'", "Get-Item 'missing-file'", "cmd /c exit 9"} {
		t.Run(command, func(t *testing.T) {
			res := Run(t.Context(), Options{Shell: "powershell", Command: command})
			if res.Exit == "" {
				t.Fatalf("error reported success: %+v", res)
			}
		})
	}
	res := Run(t.Context(), Options{Shell: "powershell", Command: "[Console]::Write((Get-Location).Path)"})
	wd, _ := os.Getwd()
	if res.Exit != "" || !strings.EqualFold(strings.TrimSpace(res.Output), wd) {
		t.Fatalf("%+v, cwd %q", res, wd)
	}
}

func TestWindowsTimeoutAndCancel(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		name := "timeout"
		if cancel {
			name = "cancel"
		}
		t.Run(name, func(t *testing.T) {
			ctx, stop := context.WithCancel(t.Context())
			defer stop()
			timeout := 500 * time.Millisecond
			if cancel {
				timeout = 20 * time.Second
				timer := time.AfterFunc(500*time.Millisecond, stop)
				defer timer.Stop()
			}
			start := time.Now()
			res := Run(ctx, Options{Shell: "powershell", Command: "Start-Sleep 60", Timeout: timeout})
			if !res.Killed || res.TimedOut == cancel || time.Since(start) > 10*time.Second {
				t.Fatalf("%+v", res)
			}
		})
	}
}

func TestWindowsKillAll(t *testing.T) {
	done := make(chan Result, 1)
	go func() { done <- Run(t.Context(), Options{Shell: "powershell", Command: "Start-Sleep 60"}) }()
	t.Cleanup(KillAll)
	deadline := time.Now().Add(5 * time.Second)
	var child *exec.Cmd
	for time.Now().Before(deadline) {
		trackMu.Lock()
		for _, cmd := range tracked {
			child = cmd
		}
		trackMu.Unlock()
		if child != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if child == nil {
		t.Fatal("child not started")
	}
	KillAll()
	select {
	case res := <-done:
		if res.Exit == "" || process.Alive(child) {
			t.Fatalf("child survived: %+v", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("KillAll did not unblock Run")
	}
}

func TestWindowsCancelKillsDescendants(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	res := Run(ctx, Options{
		Shell:   "powershell",
		Command: `$child = Start-Process powershell.exe -WindowStyle Hidden -ArgumentList '-NoProfile -NonInteractive -Command Start-Sleep 60' -PassThru; [Console]::WriteLine($child.Id); Start-Sleep 60`,
		Timeout: 10 * time.Second,
		OnUpdate: func(out string) {
			if strings.TrimSpace(out) != "" {
				cancel()
			}
		},
	})
	pid, err := strconv.Atoi(strings.TrimSpace(res.Output))
	if err != nil {
		t.Fatalf("missing child PID: %+v", res)
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	defer p.Release()
	if process.Alive(&exec.Cmd{Process: p}) {
		_ = p.Kill()
		t.Fatal("descendant survived cancellation")
	}
	if !res.Killed || res.TimedOut {
		t.Fatalf("%+v", res)
	}
}

func TestWindowsShellFullPath(t *testing.T) {
	path := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	res := Run(t.Context(), Options{Shell: path, Command: "Write-Output 'path-ok'"})
	if res.Exit != "" || !strings.Contains(res.Output, "path-ok") {
		t.Fatalf("%+v", res)
	}
}

func TestWindowsShellQuoting(t *testing.T) {
	for _, tt := range []struct{ name, command, want string }{
		{"powershell", "Write-Output 'a b \"c\" & 中文'", "a b \"c\" & 中文"},
		{"cmd", `echo "a b"`, `"a b"`},
		{"bash", `printf '%s' 'a b "c" & 中文'`, "a b \"c\" & 中文"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cmd, err := shellCommand(t.Context(), tt.name, tt.command)
			if err != nil {
				t.Fatal(err)
			}
			if cmd.Err != nil {
				t.Skip(cmd.Err)
			}
			res := Run(t.Context(), Options{Shell: tt.name, Command: tt.command})
			if res.Exit != "" || strings.TrimSpace(res.Output) != tt.want {
				t.Fatalf("%+v want %q", res, tt.want)
			}
		})
	}
}

func TestWindowsInteractiveReportsUnsupported(t *testing.T) {
	res := Run(t.Context(), Options{Shell: "powershell", Command: "Read-Host", Interactive: true})
	if !strings.Contains(res.Exit, "PTY") {
		t.Fatalf("%+v", res)
	}
}
