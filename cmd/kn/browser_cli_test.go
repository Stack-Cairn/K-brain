package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/browser/extrelay"
)

func TestBrowserCLIDispatch(t *testing.T) {
	if err := browserCLI(nil); err == nil {
		t.Error("bare `kn browser` should print usage")
	}
	if err := browserCLI([]string{"bogus"}); err == nil {
		t.Error("unknown subcommand should error")
	}
}

func TestBrowserInstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("PATH", t.TempDir())

	var err error
	out := captureStdout(t, func() { err = browserCLI([]string{"install"}) })
	if err != nil {
		t.Fatalf("install: %v", err)
	}

	dir := extrelay.ExtensionDir(home)
	entries, rerr := os.ReadDir(dir)
	if rerr != nil || len(entries) == 0 {
		t.Fatalf("extension dir not written: %v", rerr)
	}
	if _, err := os.Stat(filepath.Join(dir, "manifest.json")); err != nil {
		t.Errorf("manifest.json missing: %v", err)
	}

	statePath := extrelay.RelayStatePath(home)
	info, serr := os.Stat(statePath)
	if serr != nil {
		t.Fatalf("relay state missing: %v", serr)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("relay state should be 0600, got %v", info.Mode().Perm())
	}
	data, _ := os.ReadFile(statePath)
	var state struct{ Addr, Token string }
	if err := json.Unmarshal(data, &state); err != nil || state.Token == "" || state.Addr == "" {
		t.Errorf("relay state should carry addr+token: %v %q", err, data)
	}

	if !strings.Contains(out, dir) || !strings.Contains(out, "Load unpacked") {
		t.Errorf("install output should walk through the manual load:\n%s", out)
	}
}

func TestBrowserInstallHomeErrors(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	t.Setenv("HOME", "")

	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	if err := browserCLI([]string{"install"}); err == nil {
		t.Error("install without a home directory should error")
	}

	file := filepath.Join(t.TempDir(), "home-is-a-file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", file)
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	err := browserCLI([]string{"install"})
	if err == nil || !strings.Contains(err.Error(), "write extension") {
		t.Errorf("an unwritable home should fail on the extension write, got %v", err)
	}
}

func TestBrowserInstallRelayStateError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("PATH", t.TempDir())

	state := extrelay.RelayStatePath(home)
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}

	var err error
	_ = captureStdout(t, func() { err = browserCLI([]string{"install"}) })
	if err == nil || !strings.Contains(err.Error(), "write relay state") {
		t.Errorf("an unwritable relay state should fail the install, got %v", err)
	}
}
