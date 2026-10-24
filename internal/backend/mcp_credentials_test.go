package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/mcp"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func TestMCPRedactedHTTPSettingsRoundTripPreservesCredentials(t *testing.T) {
	root := t.TempDir()
	store, err := session.Open(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	live, err := mcp.NewLiveManager(context.Background(), filepath.Join(root, "mcp.json"), mcp.LiveSettings{Servers: []mcp.LiveServer{{
		ID: "docs", Transport: "http", URL: "https://example.invalid/mcp",
		Headers: map[string]string{"Authorization": "secret-header"}, Env: map[string]string{"TOKEN": "secret-env"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	service, err := New(Options{Store: store, Token: "fixture-token", EventDir: filepath.Join(root, "events"), MCP: live,
		Factory: func(context.Context, string, protocol.ModelRef) (*agent.Agent, error) {
			return agent.New(&scriptedClient{response: "ok"}, "fixture", 4096, "system"), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	get := httptest.NewRequest(http.MethodGet, "/v1/mcp", nil)
	get.Header.Set("Authorization", "Bearer fixture-token")
	response := httptest.NewRecorder()
	service.ServeHTTP(response, get)
	if response.Code != http.StatusOK || bytes.Contains(response.Body.Bytes(), []byte("secret-")) {
		t.Fatalf("GET status %d or credential leak", response.Code)
	}
	var document struct {
		Settings mcp.LiveSettings `json:"settings"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	document.Settings.Servers[0].Description = "Visible in LiveAgent"
	body, err := json.Marshal(document.Settings)
	if err != nil {
		t.Fatal(err)
	}
	put := httptest.NewRequest(http.MethodPut, "/v1/mcp", bytes.NewReader(body))
	put.Header.Set("Authorization", "Bearer fixture-token")
	response = httptest.NewRecorder()
	service.ServeHTTP(response, put)
	if response.Code != http.StatusOK {
		t.Fatalf("PUT status %d: %s", response.Code, response.Body)
	}
	server := live.Settings().Servers[0]
	if server.Headers["Authorization"] != "secret-header" || server.Env["TOKEN"] != "secret-env" {
		t.Fatal("HTTP settings round trip erased credentials")
	}
	if bytes.Contains(response.Body.Bytes(), []byte("secret-")) {
		t.Fatal("PUT response leaked credentials")
	}
}
