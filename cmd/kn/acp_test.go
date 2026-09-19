package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/config"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestAcpCLIConfigErrors(t *testing.T) {

	t.Run("unparseable config", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("K_BRAIN_HOME", home)
		writeConfig(t, home, `{ not json`)
		if err := acpCLI(nil); err == nil {
			t.Error("want config.Load parse error")
		}
	})

	t.Run("unknown model", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("K_BRAIN_HOME", home)
		writeConfig(t, home, `{
  "defaultModel": "test",
  "providers": {
    "testprov": {
      "baseUrl": "http://127.0.0.1:1",
      "api": "openai-completions",
      "apiKey": "k",
      "models": [
        {
          "id": "test",
          "maxTokens": 100
        }
      ]
    }
  }
}`)
		if err := acpCLI([]string{"-m", "ghost"}); err == nil {
			t.Error("want Resolve error for unknown model")
		}
	})

	t.Run("no api key", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("K_BRAIN_HOME", home)
		writeConfig(t, home, `{
  "defaultModel": "test",
  "providers": {
    "testprov": {
      "baseUrl": "http://127.0.0.1:1",
      "api": "openai-completions",
      "models": [
        {
          "id": "test",
          "maxTokens": 100
        }
      ]
    }
  }
}`)
		err := acpCLI(nil)
		if err == nil || !strings.Contains(err.Error(), "no API key") {
			t.Errorf("want no-API-key error, got %v", err)
		}
	})
}

func writeConfig(t *testing.T, home, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAcpCLIReportsSessionStorageFailure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("K_BRAIN_HOME", home)
	writeConfig(t, home, `{"defaultModel":"test","providers":{"test":{"baseUrl":"http://127.0.0.1:1/v1","api":"openai-completions","apiKey":"k","models":[{"id":"test","maxTokens":100}]}}}`)
	if err := os.WriteFile(filepath.Join(home, "sessions"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	empty, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdin
	os.Stdin = empty
	t.Cleanup(func() { os.Stdin = previous; _ = empty.Close() })
	if err := acpCLI(nil); err == nil || !strings.Contains(err.Error(), "session storage") {
		t.Fatalf("storage failure = %v", err)
	}
}

func TestAcpCLIServeExitsOnEOF(t *testing.T) {
	home := t.TempDir()
	t.Setenv("K_BRAIN_HOME", home)
	initialize := make(chan struct{})
	listed := make(chan struct{})
	var releaseOnce, listedOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(initialize) }) }
	srv := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "acp-mcp"}, nil)
	srv.AddReceivingMiddleware(func(next sdkmcp.MethodHandler) sdkmcp.MethodHandler {
		return func(ctx context.Context, method string, req sdkmcp.Request) (sdkmcp.Result, error) {
			if method == "initialize" {
				select {
				case <-initialize:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			result, err := next(ctx, method, req)
			if method == "tools/list" && err == nil {
				listedOnce.Do(func() { close(listed) })
			}
			return result, err
		}
	})
	hs := httptest.NewServer(sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return srv }, nil))
	t.Cleanup(hs.Close)
	t.Cleanup(release)
	writeConfig(t, home, fmt.Sprintf(`{"mcp":{"remote":{"url":%q}},`, hs.URL)+`
  "defaultModel": "test",
  "providers": {
    "testprov": {
      "baseUrl": "http://127.0.0.1:1",
      "api": "openai-completions",
      "apiKey": "k",
      "models": [
        {
          "id": "test",
          "maxTokens": 100
        }
      ]
    }
  }
}`)

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldIn, oldOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = inR, outW
	t.Cleanup(func() {
		os.Stdin, os.Stdout = oldIn, oldOut
		_ = inR.Close()
		_ = outW.Close()
		_ = outR.Close()
	})

	done := make(chan error, 1)
	go func() { done <- acpCLI(nil) }()

	initReq := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1,"clientCapabilities":{}}}` + "\n"
	if _, err := inW.WriteString(initReq); err != nil {
		t.Fatal(err)
	}
	readLine := func() []byte {
		got := make(chan []byte, 1)
		go func() {
			buf := make([]byte, 64<<10)
			n, _ := outR.Read(buf)
			got <- buf[:n]
		}()
		select {
		case b := <-got:
			return b
		case <-time.After(10 * time.Second):
			t.Fatal("no response from acp serve loop")
			return nil
		}
	}
	if got := readLine(); !strings.Contains(string(got), `"protocolVersion"`) {
		t.Errorf("initialize response = %q", got)
	}

	cwd, _ := os.Getwd()
	newReq := `{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":` + fmt.Sprintf("%q", cwd) + `,"mcpServers":[]}}` + "\n"
	if _, err := inW.WriteString(newReq); err != nil {
		t.Fatal(err)
	}
	if got := readLine(); !strings.Contains(string(got), `"sessionId"`) {
		t.Errorf("session/new response = %q", got)
	}
	release()
	select {
	case <-listed:
	case <-time.After(10 * time.Second):
		t.Fatal("MCP startup did not survive the session/new response")
	}

	if err := inW.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("acpCLI = %v, want nil on stdin EOF", err)
		}
	case <-time.After(10 * time.Second):
		t.Error("acpCLI did not exit after stdin EOF")
	}
}

func TestAcpSupportsVision(t *testing.T) {
	home := t.TempDir()
	t.Setenv("K_BRAIN_HOME", home)

	catalogs := `{"testprov": {"baseUrl": "http://x", "models": [
		{"id": "vis", "inputModalities": ["text", "image"]},
		{"id": "plain", "inputModalities": ["text"]}
	]}}`
	if err := os.WriteFile(filepath.Join(home, "models.json"), []byte(catalogs), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{Models: map[string]config.Model{
		"cfgvis": {Providers: []string{"testprov"}, Vision: true},
		"cfgno":  {Providers: []string{"testprov"}},
	}}

	cases := []struct {
		name, modelName, modelID string
		want                     bool
	}{
		{"catalog says image", "anyname", "vis", true},
		{"catalog says text-only", "anyname", "plain", false},
		{"no catalog entry, config true", "cfgvis", "unknown-id", true},
		{"no catalog entry, config false", "cfgno", "unknown-id", false},
		{"no catalog entry, model unknown", "ghost", "unknown-id", false},
	}
	for _, c := range cases {
		if got := acpSupportsVision(cfg, c.modelName, c.modelID, "testprov"); got != c.want {
			t.Errorf("%s: acpSupportsVision = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestAcpSupportsVisionNoCatalog(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	cfg := &config.Config{Models: map[string]config.Model{
		"m": {Providers: []string{"p"}, Vision: true},
	}}
	if !acpSupportsVision(cfg, "m", "any-id", "p") {
		t.Error("config vision=true should hold when there's no catalog")
	}
}

func TestAcpBaseMCP(t *testing.T) {
	home := t.TempDir()
	t.Setenv("K_BRAIN_HOME", home)

	empty := acpBaseMCP(&config.Config{})
	if len(empty) != 0 {
		t.Errorf("empty config: got %v", empty)
	}

	cfg := &config.Config{
		MCPServers: map[string]config.MCPServer{
			"docs": {Command: []string{"docs-mcp", "--serve"}},
		},
	}
	got := acpBaseMCP(cfg)
	srv, ok := got["docs"]
	if !ok {
		t.Fatalf("docs server missing: %v", got)
	}
	if len(srv.Command) != 2 || srv.Command[0] != "docs-mcp" {
		t.Errorf("docs command = %v", srv.Command)
	}
}
