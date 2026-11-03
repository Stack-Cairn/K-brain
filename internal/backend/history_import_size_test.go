package backend

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func TestLegacyHistoryImportAcceptsBodiesAboveGenericLimit(t *testing.T) {
	store, err := session.Open(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, server := newTestServer(t, store, t.TempDir(), &scriptedClient{response: "unused"})
	created := time.Unix(100, 0).UTC()
	large := strings.Repeat("x", 3<<20)
	metadata, err := json.Marshal(map[string]any{"original": large, "segments": []map[string]any{{"messages_json": large}}})
	if err != nil {
		t.Fatal(err)
	}
	input := legacyHistoryImportRequest{
		SourceID: "legacy-large", SourceFingerprint: "fixture-fingerprint", ConversationID: "legacy-large",
		Title: "Large", CWD: t.TempDir(), Model: protocol.ModelRef{Provider: "fixture", Model: "fixture-model"},
		CreatedAt: created, UpdatedAt: created, SourceMetadata: metadata,
		Messages: []protocol.Message{{ID: "user-1", Role: protocol.RoleUser, CreatedAt: &created, Content: []protocol.ContentBlock{{Type: protocol.ContentText, Text: large}}}},
	}
	var result map[string]any
	response := postJSON(t, http.DefaultClient, server.URL+"/v1/migrations/liveagent-history", input, &result)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || result["status"] != "imported" || result["fingerprint"] != importFingerprint(input) {
		t.Fatalf("large import = %d %#v", response.StatusCode, result)
	}
	meta, messages, err := store.Load("legacy-large")
	if err != nil || len(messages) != 1 || meta.ImportMetadata != string(metadata) {
		t.Fatalf("stored import = %v messages=%d metadata=%d", err, len(messages), len(meta.ImportMetadata))
	}
}

// Oversized bodies must get a readable 413, not a connection reset that browsers report as "Failed to fetch".
func TestOversizedJSONBodiesReturnReadable413(t *testing.T) {
	store, err := session.Open(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, server := newTestServer(t, store, t.TempDir(), &scriptedClient{response: "unused"})
	for _, tc := range []struct {
		name, path string
		size       int64
		limit      string
	}{
		{name: "generic", path: "/v1/sessions", size: maxBodyBytes + 1<<20, limit: "4 MiB"},
		{name: "migration", path: "/v1/migrations/liveagent-history", size: maxLegacyHistoryBodyBytes + 1<<20, limit: "512 MiB"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Write the whole body before reading, as browsers do; a server that
			// closes with unread bytes surfaces here as a reset write.
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", strings.TrimPrefix(server.URL, "http://"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if closeErr := conn.Close(); closeErr != nil {
					t.Errorf("closing oversized request connection: %v", closeErr)
				}
			}()
			_ = conn.SetDeadline(time.Now().Add(time.Minute))
			prefix, suffix := `{"source_metadata":"`, `"}`
			if _, err := fmt.Fprintf(conn, "POST %s HTTP/1.1\r\nHost: kbrain\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", tc.path, int64(len(prefix)+len(suffix))+tc.size, prefix); err != nil {
				t.Fatal(err)
			}
			chunk := bytes.Repeat([]byte("a"), 1<<20)
			for range tc.size >> 20 {
				if _, err := conn.Write(chunk); err != nil {
					t.Fatalf("server reset the connection while the body was being sent: %v", err)
				}
			}
			if _, err := io.WriteString(conn, suffix); err != nil {
				t.Fatalf("server reset the connection while the body was being sent: %v", err)
			}
			resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
			if err != nil {
				t.Fatalf("oversized request was not answered: %v", err)
			}
			defer resp.Body.Close()
			var body map[string]any
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != http.StatusRequestEntityTooLarge || !strings.Contains(body["error"].(string), tc.limit) {
				t.Fatalf("oversized response = %d %#v", resp.StatusCode, body)
			}
		})
	}
}
