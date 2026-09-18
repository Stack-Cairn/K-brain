package mcp

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStdioServerEndToEnd(t *testing.T) {
	if os.Getenv("K_BRAIN_TEST_SELFHOST") == "" {
		t.Skip("set K_BRAIN_TEST_SELFHOST=1 to run")
	}
	bin := filepath.Join(t.TempDir(), "k-brain")
	if out, err := exec.CommandContext(context.Background(), "go", "build", "-o", bin, "../../cmd/kn").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	m := NewManager(map[string]ServerConfig{"self": {Command: []string{bin, "mcp", "serve"}}})
	m.Start(context.Background())
	s := m.servers["self"]
	select {
	case <-s.ready:
	case <-time.After(30 * time.Second):
		t.Fatal("never settled")
	}
	if st := m.Statuses()[0]; st.Status != StatusReady || st.Tools != 4 {
		t.Fatalf("status = %+v", st)
	}
	out, err := s.call(context.Background(), "read", json.RawMessage(`{"path":"manager.go","limit":3}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "package mcp") {
		t.Fatalf("read via MCP = %q", out)
	}
	m.Close()
	if st := m.Statuses()[0]; st.Status == StatusReady {
		t.Error("post-Close status should not be ready")
	}

	m2 := NewManager(map[string]ServerConfig{"bad": {Command: []string{"sh", "-c", "echo dying-loudly >&2; exit 1"}, StartupTimeout: 5}})
	m2.Start(context.Background())
	defer m2.Close()
	s2 := m2.servers["bad"]
	select {
	case <-s2.ready:
	case <-time.After(10 * time.Second):
		t.Fatal("never settled")
	}
	st := m2.Statuses()[0]
	if st.Status != StatusFailed {
		t.Fatalf("bad server status = %+v", st)
	}
	if !strings.Contains(st.Err, "dying-loudly") {
		t.Errorf("stderr tail should be in the failure message: %q", st.Err)
	}
}
