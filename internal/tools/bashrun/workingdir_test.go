package bashrun

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRunWorkingDirectory(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	for _, item := range []struct{ dir, text string }{{first, "context"}, {second, "option"}} {
		if err := os.WriteFile(filepath.Join(item.dir, "marker.txt"), []byte(item.text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	shell, command := "bash", "cat marker.txt"
	if runtime.GOOS == "windows" {
		shell, command = "powershell", "Get-Content -LiteralPath marker.txt"
	}
	ctx := WithWorkingDir(context.Background(), first)
	for _, item := range []struct{ dir, want string }{{"", "context"}, {second, "option"}} {
		res := Run(ctx, Options{Shell: shell, Command: command, Dir: item.dir})
		if res.Exit != "" || strings.TrimSpace(res.Output) != item.want {
			t.Fatalf("working directory %q: %+v", item.dir, res)
		}
	}
}
