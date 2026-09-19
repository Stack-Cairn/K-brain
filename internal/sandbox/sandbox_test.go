package sandbox

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDisabledPolicyLeavesCommandUntouched(t *testing.T) {
	cmd := exec.Command("echo", "ok")
	got, err := New("off", "", t.TempDir(), false, nil, nil).Wrap(context.Background(), cmd)
	if err != nil || got != cmd {
		t.Fatalf("disabled policy changed command: %v", err)
	}
}

func TestCommandWorkingDirectory(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	for _, item := range []struct{ dir, want string }{{"", root}, {"nested", filepath.Join(root, "nested")}, {other, other}} {
		cmd := exec.Command("echo", "ok")
		cmd.Dir = item.dir
		if got := commandDir(cmd, root); got != item.want {
			t.Fatalf("directory %q: got %q, want %q", item.dir, got, item.want)
		}
	}
}

func TestEnabledPolicyRequiresBackend(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native Windows backend is environment dependent")
	}
	_, _ = New("strict", "", t.TempDir(), false, nil, nil).Wrap(context.Background(), exec.Command("echo", "ok"))
}
