package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/update"
)

func TestMain(m *testing.M) {
	if output := os.Getenv("K_BRAIN_INSTALLER_TEST_ARGS"); output != "" {
		exe, err := os.Executable()
		if err == nil && (filepath.Base(exe) == "sh" || filepath.Base(exe) == "powershell.exe") {
			data, err := json.Marshal(os.Args[1:])
			if err != nil || os.WriteFile(output, data, 0600) != nil {
				os.Exit(99)
			}
			code, err := strconv.Atoi(os.Getenv("K_BRAIN_INSTALLER_TEST_EXIT"))
			if err != nil {
				os.Exit(98)
			}
			os.Exit(code)
		}
	}
	os.Exit(m.Run())
}

func stubShell(t *testing.T, exitCode string) (argsFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args.txt")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	name := "sh"
	if runtime.GOOS == "windows" {
		name = "powershell.exe"
	}
	out, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(out, in)
	if err := errors.Join(copyErr, out.Close()); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	t.Setenv("K_BRAIN_INSTALLER_TEST_ARGS", argsFile)
	t.Setenv("K_BRAIN_INSTALLER_TEST_EXIT", exitCode)
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
	url := update.InstallURL
	if runtime.GOOS == "windows" {
		url = strings.TrimSuffix(url, "sh") + "ps1"
	}
	if !strings.Contains(string(args), url) {
		t.Errorf("installer command should use %s, got %q", url, args)
	}
}

func TestUpdateCLIInstallerFails(t *testing.T) {
	argsFile := stubShell(t, "3")

	var err error
	out := captureStdout(t, func() { err = updateCLI() })
	if err == nil || !strings.Contains(err.Error(), "update failed") {
		t.Fatalf("a failing installer should surface as an update error, got %v", err)
	}
	if strings.Contains(out, "k-brain updated") {
		t.Errorf("failure must not claim success:\n%s", out)
	}
	if _, err := os.Stat(argsFile); err != nil {
		t.Fatalf("installer fixture did not execute: %v", err)
	}
}
