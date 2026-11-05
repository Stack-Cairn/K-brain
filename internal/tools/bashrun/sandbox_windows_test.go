//go:build windows

package bashrun

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/sandbox"
)

// TestSandboxedShellCommands runs the tool path end to end: the shell command
// line (cmd.exe's raw CmdLine included), the environment set after Wrap and the
// tree kill on timeout all go through the native sandbox launcher.
func TestSandboxedShellCommands(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	t.Setenv("K_BRAIN_HOME", "")
	root, outside := t.TempDir(), t.TempDir()
	policy := &sandbox.Policy{Mode: "workspace", Backend: "auto", Root: root, Network: true, Masked: sandbox.MaskedDirs(), Protected: sandbox.ProtectedPaths(root)}
	powershell := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	for _, tc := range []struct{ shell, command string }{
		{"cmd", `echo in> "` + filepath.Join(root, "cmd.txt") + `" & echo out> "` + filepath.Join(outside, "cmd.txt") + `"`},
		{powershell, `Set-Content -Path '` + filepath.Join(root, "powershell.txt") + `' -Value in; try { Set-Content -Path '` + filepath.Join(outside, "powershell.txt") + `' -Value out } catch { Write-Output "denied" }`},
	} {
		name := ShellName(tc.shell)
		t.Run(name, func(t *testing.T) {
			result := Run(context.Background(), Options{Command: tc.command, Shell: tc.shell, Dir: root, Timeout: time.Minute, Sandbox: policy})
			t.Logf("exit=%q output=%q", result.Exit, result.Output)
			if _, err := os.Stat(filepath.Join(root, name+".txt")); err != nil {
				t.Fatalf("%s could not write the workspace: %v (%s)", name, err, result.Output)
			}
			if _, err := os.Stat(filepath.Join(outside, name+".txt")); err == nil {
				t.Fatalf("%s wrote outside the workspace", name)
			}
		})
	}
	t.Run("timeout kills the tree", func(t *testing.T) {
		started := time.Now()
		result := Run(context.Background(), Options{Command: "ping -n 60 127.0.0.1", Shell: "cmd", Dir: root, Timeout: 2 * time.Second, Sandbox: policy})
		if !result.TimedOut || time.Since(started) > 30*time.Second {
			t.Fatalf("sandboxed command not stopped on timeout: %+v after %s", result, time.Since(started))
		}
	})
}
