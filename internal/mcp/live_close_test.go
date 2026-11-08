package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

// Replacing the settings retires the old manager, and closing a remote server
// that never answered initialize takes seconds. That close used to run under
// the LiveManager lock, so every run start (ToolsFor) and every MCP status
// request waited for it.
func TestReplaceClosesTheOldManagerOutsideTheLock(t *testing.T) {
	release := make(chan struct{})
	wedged := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer wedged.Close()
	defer close(release)

	root := t.TempDir()
	live, err := NewLiveManager(context.Background(), filepath.Join(root, "mcp.json"), LiveSettings{Servers: []LiveServer{{ID: "wedged", Enabled: true, Transport: "http", URL: wedged.URL, TimeoutMs: 600000}}})
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	// Let the connect reach the wedged initialize.
	time.Sleep(300 * time.Millisecond)

	replaced := make(chan error, 1)
	go func() { replaced <- live.Replace(context.Background(), LiveSettings{}) }()
	time.Sleep(300 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = live.Statuses()
		_ = live.ToolsFor(context.Background(), root, nil)
		_ = live.Settings()
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Statuses/ToolsFor/Settings waited for the retired manager to close")
	}
	select {
	case err := <-replaced:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Replace did not finish")
	}
}
