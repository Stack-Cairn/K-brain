package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/update"
)

func stubShell(t *testing.T, exitCode string) (argsFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args.txt")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\nexit " + exitCode + "\n"
	if err := os.WriteFile(filepath.Join(dir, "sh"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return argsFile
}

func TestUpdateCLIRunsInstaller(t *testing.T) {
	argsFile := stubShell(t, "0")

	var err error
	out := captureStdout(t, func() { err = updateCLI() })
	if err != nil {
		t.Fatalf("update with a succeeding installer: %v", err)
	}
	if !strings.Contains(out, "k-brain updated") {
		t.Errorf("success message missing:\n%s", out)
	}
	args, rerr := os.ReadFile(argsFile)
	if rerr != nil {
		t.Fatalf("stub sh never ran: %v", rerr)
	}
	if !strings.Contains(string(args), update.InstallURL) {
		t.Errorf("installer command should pipe %s, got %q", update.InstallURL, args)
	}
}

func TestUpdateCLIInstallerFails(t *testing.T) {
	stubShell(t, "3")

	var err error
	out := captureStdout(t, func() { err = updateCLI() })
	if err == nil || !strings.Contains(err.Error(), "update failed") {
		t.Fatalf("a failing installer should surface as an update error, got %v", err)
	}
	if strings.Contains(out, "k-brain updated") {
		t.Errorf("failure must not claim success:\n%s", out)
	}
}
