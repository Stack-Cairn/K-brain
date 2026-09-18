package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/mcp"
)

func importFixture(t *testing.T, mcpImport string) (wd string) {
	t.Helper()
	kBrainHome := t.TempDir()
	t.Setenv("K_BRAIN_HOME", kBrainHome)
	wd = t.TempDir()
	cfgSrc := `{
  "defaultModel": "m1",
  "providers": { "a": { "baseUrl": "https://a", "api": "openai-completions", "models": [{"id": "m1"}] } }
  ` + mcpImport + `
}`
	if err := os.WriteFile(filepath.Join(kBrainHome, "config.json"), []byte(cfgSrc), 0o600); err != nil {
		t.Fatal(err)
	}
	codexFile := filepath.Join(wd, "codex.toml")
	if err := os.WriteFile(codexFile, []byte(
		"[mcp_servers.node_repl]\ncommand = \"/app/bin/node_repl\"\n[mcp_servers.paper]\nurl = \"http://127.0.0.1:29979/mcp\"\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	orig := mcp.CodexPath
	mcp.CodexPath = func() string { return codexFile }
	t.Cleanup(func() { mcp.CodexPath = orig })

	origG := mcp.ClaudeGlobalPath
	mcp.ClaudeGlobalPath = func() string { return filepath.Join(wd, "absent-claude.json") }
	t.Cleanup(func() { mcp.ClaudeGlobalPath = origG })
	return wd
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	t.Chdir(dir)
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origOut := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = origOut }()
	fn()
	w.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestMCPImportDryRunWritesNothing(t *testing.T) {
	wd := importFixture(t, "")
	chdir(t, wd)

	var runErr error
	printed := captureStdout(t, func() { runErr = mcpImportCLI([]string{"--dry-run"}) })
	if runErr != nil {
		t.Fatal(runErr)
	}
	if !strings.Contains(printed, "node_repl") || !strings.Contains(printed, "paper") {
		t.Errorf("dry-run should list both imported servers:\n%s", printed)
	}

	start := strings.Index(printed, "{")
	if start < 0 {
		t.Fatalf("no JSON fragment printed:\n%s", printed)
	}
	var fragment map[string]config.MCPServer
	if err := json.Unmarshal([]byte(strings.TrimSpace(printed[start:])), &fragment); err != nil {
		t.Errorf("fragment should parse as mcp entries: %v\n%s", err, printed[start:])
	}
	if fragment["paper"].URL != "http://127.0.0.1:29979/mcp" {
		t.Errorf("fragment lost the url: %+v", fragment["paper"])
	}

	reloaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.MCPServers) != 0 {
		t.Errorf("dry-run must not mutate the config, got %+v", reloaded.MCPServers)
	}
}

func TestMCPImportAppliesAndIsIdempotent(t *testing.T) {
	wd := importFixture(t, `, "mcpImport": { "codex": { "exclude": ["node_repl"] } }`)
	chdir(t, wd)

	if err := mcpImportCLI(nil); err != nil {
		t.Fatal(err)
	}
	reloaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.MCPServers["paper"].URL != "http://127.0.0.1:29979/mcp" {
		t.Errorf("paper should be imported, got %+v", reloaded.MCPServers)
	}
	if _, ok := reloaded.MCPServers["node_repl"]; ok {
		t.Error("blocked servers are never imported")
	}

	var runErr error
	printed := captureStdout(t, func() { runErr = mcpImportCLI(nil) })
	if runErr != nil {
		t.Fatal(runErr)
	}
	if !strings.Contains(printed, "nothing to import") {
		t.Errorf("second run should be a no-op, got %q", printed)
	}
	reloaded2, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded2.MCPServers) != 1 {
		t.Errorf("config should hold exactly the imported entry, got %+v", reloaded2.MCPServers)
	}
}
