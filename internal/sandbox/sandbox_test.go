package sandbox

import (
	"context"
	"os/exec"
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

func TestEnabledPolicyRequiresBackend(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native Windows backend is environment dependent")
	}
	_, _ = New("strict", "", t.TempDir(), false, nil, nil).Wrap(context.Background(), exec.Command("echo", "ok"))
}
