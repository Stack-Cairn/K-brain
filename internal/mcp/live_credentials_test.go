package mcp

import (
	"context"
	"path/filepath"
	"testing"
)

func TestLiveReplacePreservesRedactedCredentials(t *testing.T) {
	settings := LiveSettings{Servers: []LiveServer{{
		ID: "docs", Transport: "http", URL: "https://example.invalid/mcp",
		Env:     map[string]string{"TOKEN": "env-secret"},
		Headers: map[string]string{"Authorization": "header-secret"},
		Auth:    &LiveAuth{Type: "oauth", CredentialRef: "native-keychain"},
	}}}
	path := filepath.Join(t.TempDir(), "mcp.json")
	live, err := NewLiveManager(context.Background(), path, settings)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	redacted := live.Settings()
	redacted.Servers[0] = redactLiveServer(redacted.Servers[0])
	redacted.Servers[0].Description = "Updated in LiveAgent"
	redacted.Servers = append(redacted.Servers, LiveServer{ID: "imported", Transport: "stdio", Command: "server"})
	if err := live.Replace(context.Background(), redacted); err != nil {
		t.Fatal(err)
	}
	stored, err := loadLiveSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	server := stored.Servers[0]
	if server.Env["TOKEN"] != "env-secret" || server.Headers["Authorization"] != "header-secret" || server.Auth.CredentialRef != "native-keychain" {
		t.Fatal("redacted round trip erased credentials")
	}
	cleared := live.Settings()
	cleared.Servers[0].Env = map[string]string{}
	cleared.Servers[0].Headers = map[string]string{}
	cleared.Servers[0].Auth = &LiveAuth{Type: "none"}
	if err := live.Replace(context.Background(), cleared); err != nil {
		t.Fatal(err)
	}
	server = live.Settings().Servers[0]
	if len(server.Env) != 0 || len(server.Headers) != 0 || server.Auth.CredentialRef != "" {
		t.Fatal("explicit clear did not remove credentials")
	}
}
