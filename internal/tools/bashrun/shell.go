package bashrun

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf16"
)

type shellKey struct{}

func WithShell(ctx context.Context, shell string) context.Context {
	return context.WithValue(ctx, shellKey{}, shell)
}

func DefaultShell() string { return userShell() }

func shellCommand(ctx context.Context, shell, command string) (*exec.Cmd, error) {
	if shell == "" {
		shell = userShell()
	}
	name := strings.TrimSuffix(strings.ToLower(filepath.Base(shell)), ".exe")
	var args []string
	switch name {
	case "powershell7":
		shell = "pwsh"
		fallthrough
	case "powershell", "pwsh":

		script := "$ProgressPreference = 'SilentlyContinue'; [Console]::InputEncoding = [Console]::OutputEncoding = $OutputEncoding = [System.Text.UTF8Encoding]::new(); " +
			"$ErrorActionPreference = 'Stop'; & {\n" + command + "\n}; " +
			"if (-not $?) { exit 1 }; if ($null -ne $LASTEXITCODE) { exit $LASTEXITCODE }"
		words := utf16.Encode([]rune(script))
		data := make([]byte, len(words)*2)
		for i, word := range words {
			binary.LittleEndian.PutUint16(data[i*2:], word)
		}
		args = []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-OutputFormat", "Text", "-EncodedCommand", base64.StdEncoding.EncodeToString(data)}
	case "cmd":
		args = []string{"/D", "/S", "/C", command}
	case "wsl":
		if runtime.GOOS != "windows" {
			return nil, fmt.Errorf("shell wsl requires Windows")
		}

		args = []string{"--exec", "bash", "-c", command}
	case "bash":

		if runtime.GOOS == "windows" && filepath.Base(shell) == shell {
			for _, root := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs")} {
				if root == "" {
					continue
				}
				candidate := filepath.Join(root, "Git", "bin", "bash.exe")
				if _, err := os.Stat(candidate); err == nil {
					shell = candidate
					break
				}
			}
		}
		args = []string{"-c", command}
	default:
		args = []string{"-c", command}
	}
	cmd := exec.CommandContext(ctx, shell, args...)
	configureShellCommand(cmd, name, command)
	return cmd, nil
}
