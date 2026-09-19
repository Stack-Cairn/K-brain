package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

func TestMCPCLIIgnoresExternalConfiguration(t *testing.T) {
	home := mcpHome(t, `, "mcp": {"own": {"command": ["unused"], "enabled": false}}`)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		filepath.Join(home, ".codex", "config.toml"): "[mcp_servers.external_codex]\ncommand = \"unused\"\n",
		filepath.Join(home, ".claude.json"):          `{"mcpServers":{"external_global":{"command":"unused"}}}`,
		filepath.Join(wd, ".mcp.json"):               `{"mcpServers":{"external_project":{"command":"unused"}}}`,
	}
	for path, body := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	before, err := os.ReadFile(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() { err = mcpCLI([]string{"list"}, "test") })
	if err != nil || !strings.Contains(out, "own") || strings.Contains(out, "external_") {
		t.Fatalf("list read external configuration: %v %q", err, out)
	}
	for _, name := range []string{"external_codex", "external_global", "external_project"} {
		if err := mcpTestCLI(name); err == nil || !strings.Contains(err.Error(), "no mcp server") {
			t.Fatalf("external probe: %v", err)
		}
		if err := mcpCLI([]string{"remove", name}, "test"); err == nil || !strings.Contains(err.Error(), "no mcp server") {
			t.Fatalf("external removal: %v", err)
		}
	}
	if err := mcpCLI([]string{"import"}, "test"); err == nil || !strings.Contains(err.Error(), "unknown mcp subcommand") {
		t.Fatalf("removed import command: %v", err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	servers := acpBaseMCP(cfg)
	if len(servers) != 1 || servers["own"].Source != "k-brain" {
		t.Fatalf("ACP loaded external configuration: %+v", servers)
	}
	after, err := os.ReadFile(filepath.Join(home, "config.json"))
	if err != nil || string(after) != string(before) {
		t.Fatalf("read-only or rejected command changed own config: %v", err)
	}
	for path, expected := range files {
		body, err := os.ReadFile(path)
		if err != nil || string(body) != expected {
			t.Fatalf("external configuration changed: %s: %v", path, err)
		}
	}
}

func TestMCPCLIRejectsRemovedImportWithoutConfig(t *testing.T) {
	home := filepath.Join(t.TempDir(), "uninitialized")
	t.Setenv("K_BRAIN_HOME", home)
	for _, args := range [][]string{{"import"}, {"import", "--dry-run"}, {"bogus"}} {
		if err := mcpCLI(args, "test"); err == nil || !strings.Contains(err.Error(), "unknown mcp subcommand") {
			t.Fatalf("unrecognized command %v: %v", args, err)
		}
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("rejected commands initialized the configuration directory: %v", err)
	}
}

func TestMCPCLIAddListRemove(t *testing.T) {
	mcpHome(t, "")

	if err := mcpCLI(nil, "v"); err == nil {
		t.Error("bare `kn mcp` should print usage")
	}
	if err := mcpCLI([]string{"bogus"}, "v"); err == nil {
		t.Error("unknown subcommand should error")
	}
	if err := mcpCLI([]string{"add"}, "v"); err == nil {
		t.Error("add without a name should error")
	}
	if err := mcpCLI([]string{"add", "x", "oops"}, "v"); err == nil {
		t.Error("add without -- or --url should error")
	}
	if err := mcpCLI([]string{"add", "bad", "--url", "ftp://x"}, "v"); err == nil {
		t.Error("a non-http url is an invalid server")
	}

	var err error
	out := captureStdout(t, func() { err = mcpCLI([]string{"add", "local", "--", "echo", "hi"}, "v") })
	if err != nil || !strings.Contains(out, `added mcp server "local"`) {
		t.Fatalf("add stdio: %v %q", err, out)
	}
	if err := mcpCLI([]string{"add", "remote", "--url", "http://127.0.0.1:9/mcp"}, "v"); err != nil {
		t.Fatalf("add remote: %v", err)
	}

	out = captureStdout(t, func() { err = mcpCLI([]string{"list"}, "v") })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"local", "echo hi", "remote", "http://127.0.0.1:9/mcp", "k-brain config"} {
		if !strings.Contains(out, want) {
			t.Errorf("list missing %q:\n%s", want, out)
		}
	}

	if err := mcpCLI([]string{"remove", "local"}, "v"); err != nil {
		t.Fatalf("remove own server: %v", err)
	}
	out = captureStdout(t, func() { _ = mcpCLI([]string{"list"}, "v") })
	if strings.Contains(out, "local") {
		t.Errorf("removed server still listed:\n%s", out)
	}
	if err := mcpCLI([]string{"remove", "paper"}, "v"); err == nil || !strings.Contains(err.Error(), "no mcp server") {
		t.Errorf("removing an unconfigured server should fail, got %v", err)
	}
	if err := mcpCLI([]string{"remove", "nosuch"}, "v"); err == nil || !strings.Contains(err.Error(), "no mcp server") {
		t.Errorf("removing an unknown server: %v", err)
	}
	if err := mcpCLI([]string{"remove"}, "v"); err == nil {
		t.Error("remove without a name should error")
	}
}

func TestMCPTestCLIUnknownAndDisabled(t *testing.T) {
	kBrainHome := t.TempDir()
	t.Setenv("K_BRAIN_HOME", kBrainHome)
	chdir(t, t.TempDir())

	cfg := `{
  "defaultModel": "m1",
  "providers": {
    "a": {
      "baseUrl": "https://a",
      "api": "openai-completions",
      "models": [
        {
          "id": "m1"
        }
      ]
    }
  },
  "mcp": {
    "off": {
      "command": [
        "true"
      ],
      "enabled": false
    }
  }
}`
	if err := os.WriteFile(filepath.Join(kBrainHome, "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := mcpCLI([]string{"test"}, "v"); err == nil {
		t.Error("`mcp test` without a name should print usage")
	}
	var err error
	_ = captureStdout(t, func() { err = mcpTestCLI("nosuch") })
	if err == nil || !strings.Contains(err.Error(), "no mcp server named") {
		t.Errorf("unknown server: %v", err)
	}

	out := captureStdout(t, func() { err = mcpTestCLI("off") })
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Errorf("disabled server should error: %v", err)
	}
	if !strings.Contains(out, "disabled") {
		t.Errorf("disabled status not printed:\n%s", out)
	}

	out = captureStdout(t, func() { _ = mcpCLI([]string{"list"}, "v") })
	if !strings.Contains(out, "off") || !strings.Contains(out, "disabled") {
		t.Errorf("list should show the disabled server:\n%s", out)
	}
}

func mcpHome(t *testing.T, mcpBlock string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("K_BRAIN_HOME", home)
	chdir(t, t.TempDir())

	cfg := `{
  "defaultModel": "m1",
  "providers": { "a": { "baseUrl": "https://a", "api": "openai-completions", "models": [{"id": "m1"}] } }` + mcpBlock + `
}`
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestMCPCLIListEmpty(t *testing.T) {
	mcpHome(t, "")
	var err error
	out := captureStdout(t, func() { err = mcpCLI([]string{"list"}, "v") })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "no MCP servers configured") {
		t.Fatalf("empty list should say so, got %q", out)
	}
}

func TestMCPCLIRoutesTestAndImport(t *testing.T) {
	mcpHome(t, "")

	var err error
	_ = captureStdout(t, func() { err = mcpCLI([]string{"test", "nosuch"}, "v") })
	if err == nil || !strings.Contains(err.Error(), "no mcp server named") {
		t.Errorf("`mcp test <name>` should reach the doctor, got %v", err)
	}

	out := captureStdout(t, func() { err = mcpCLI([]string{"import", "--dry-run"}, "v") })
	if err == nil || !strings.Contains(err.Error(), "unknown mcp subcommand") || out != "" {
		t.Errorf("`mcp import` must be unavailable, got %v %q", err, out)
	}
}

func TestMCPTestCLIInvalidServerFails(t *testing.T) {
	mcpHome(t, `,
  "mcp": { "broken": { "note": "configured without a command" } }`)

	var err error
	out := captureStdout(t, func() { err = mcpTestCLI("broken") })
	if err == nil || !strings.Contains(err.Error(), "failed") {
		t.Fatalf("an invalid server should fail, got %v", err)
	}
	for _, want := range []string{"✗ failed", "neither command nor url", "note: configured without a command", "config:"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output missing %q:\n%s", want, out)
		}
	}
}

func TestMCPCLIUnreadableConfig(t *testing.T) {
	unusableHome(t)
	chdir(t, t.TempDir())

	if err := mcpCLI([]string{"list"}, "v"); err == nil {
		t.Error("list with an unreadable config should error")
	}
	if err := mcpTestCLI("any"); err == nil {
		t.Error("test with an unreadable config should error")
	}
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()
	fn()
	w.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestMCPCLIListIgnoresBrokenProjectFile(t *testing.T) {
	mcpHome(t, `,
  "mcp": { "ok": { "command": ["true"] } }`)
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wd, ".mcp.json"), []byte("{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out string
	errOut := captureStderr(t, func() {
		out = captureStdout(t, func() {
			if err := mcpCLI([]string{"list"}, "v"); err != nil {
				t.Error(err)
			}
		})
	})
	if errOut != "" {
		t.Errorf("an external project file must be ignored, got %q", errOut)
	}
	if !strings.Contains(out, "ok") {
		t.Errorf("the working servers should still be listed:\n%s", out)
	}
}

func TestMCPServeHelperProcess(t *testing.T) {
	if os.Getenv("K_BRAIN_MCP_SERVE_HELPER") != "1" {
		t.Skip("helper process, run only by TestMCPTestCLIReady")
	}
	if err := mcpCLI([]string{"serve"}, "helper"); err != nil {
		fmt.Fprintln(os.Stderr, "serve:", err)
	}
}

func TestMCPTestCLIReady(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Skip("no test binary path")
	}
	mcpHome(t, fmt.Sprintf(`,
  "mcp": { "self": { "command": [%q, "-test.run=^TestMCPServeHelperProcess$"], "env": { "K_BRAIN_MCP_SERVE_HELPER": "1" }, "startupTimeout": 20 } }`, self))

	out := captureStdout(t, func() {
		if terr := mcpTestCLI("self"); terr != nil {
			t.Errorf("a live server should probe clean: %v", terr)
		}
	})
	if !strings.Contains(out, "✓ connected") {
		t.Fatalf("doctor should report the connection:\n%s", out)
	}
	if !strings.Contains(out, "tools:") || !strings.Contains(out, "read") {
		t.Errorf("doctor should list the served tools:\n%s", out)
	}
}

func TestMCPCLISaveFailures(t *testing.T) {
	home := mcpHome(t, `,
  "mcp": { "mine": { "command": ["true"] } }`)
	if err := os.Mkdir(filepath.Join(home, "config.json.tmp"), 0700); err != nil {
		t.Fatal(err)
	}

	if err := mcpCLI([]string{"add", "new", "--", "true"}, "v"); err == nil {
		t.Error("add should report an unwritable config")
	}
	if err := mcpCLI([]string{"remove", "mine"}, "v"); err == nil {
		t.Error("remove should report an unwritable config")
	}
}
