package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWorkingDirectoryResolvesFileTools(t *testing.T) {
	dir := t.TempDir()
	ctx := WithWorkingDir(context.Background(), dir)
	write := findToolForTest(All(), "write")
	read := findToolForTest(All(), "read")
	edit := findToolForTest(All(), "edit")
	path := filepath.Join(dir, "nested", "note.txt")

	args, _ := json.Marshal(map[string]string{"path": filepath.Join("nested", "note.txt"), "content": "hello"})
	if out, err := write.Run(ctx, args); err != nil || !strings.Contains(out, path) {
		t.Fatalf("write in working directory: %q, %v", out, err)
	}
	readArgs, _ := json.Marshal(map[string]string{"path": filepath.Join("nested", "note.txt")})
	if out, err := read.Run(ctx, readArgs); err != nil || !strings.Contains(out, "hello") {
		t.Fatalf("read in working directory: %q, %v", out, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	editArgs, _ := json.Marshal(map[string]string{"path": filepath.Join("nested", "note.txt"), "old_string": "hello", "new_string": "updated"})
	if _, err := edit.Run(ctx, editArgs); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "updated" {
		t.Fatalf("edit in working directory: %q, %v", data, err)
	}
}

func TestWorkingDirectoryShell(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "working directory 中文")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "marker.txt"), []byte("worktree-marker"), 0o600); err != nil {
		t.Fatal(err)
	}
	shell, command := "bash", "cat marker.txt"
	if runtime.GOOS == "windows" {
		shell, command = "powershell", "Get-Content -LiteralPath marker.txt"
	}
	before, _ := os.Getwd()
	args, _ := json.Marshal(map[string]string{"shell": shell, "command": command})
	out, err := findToolForTest(All(), "bash").Run(WithWorkingDir(context.Background(), dir), args)
	if err != nil || strings.TrimSpace(out) != "worktree-marker" {
		t.Fatalf("shell working directory: %q, %v", out, err)
	}
	after, _ := os.Getwd()
	if before != after {
		t.Fatal("shell changed process-wide working directory")
	}
}

func findToolForTest(ts []Tool, name string) Tool {
	for _, tool := range ts {
		if tool.Def.Function.Name == name {
			return tool
		}
	}
	panic("missing tool " + name)
}
