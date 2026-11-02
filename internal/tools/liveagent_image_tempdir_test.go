package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Screenshots and generated images in the system temp directory display even
// though no workspace root covers them; non-images and directories there, and
// files outside both the roots and temp, are still refused.
func TestLiveAgentImageDisplaysTempDirectoryImages(t *testing.T) {
	work, outside := t.TempDir(), t.TempDir()
	ctx := WithWorkingDir(context.Background(), work)
	ts := LiveAgentCatalog()
	png, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Wl6WQAAAABJRU5ErkJggg==")
	shot := filepath.Join(outside, "shot.png")
	if err := os.WriteFile(shot, png, 0o600); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(outside, "notes.txt")
	if err := os.WriteFile(secret, []byte("token=abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, call := range []map[string]any{
		{"path": shot},
		{"path": "", "paths": []string{shot}, "url": "", "urls": []string{""}},
		{"url": "file:///" + filepath.ToSlash(shot)},
	} {
		raw, _ := json.Marshal(call)
		result := ExecuteResult(ctx, ts, "Image", raw, true)
		if result.Failed || len(result.Parts) != 1 || !strings.HasPrefix(result.Parts[0].ImageURL.URL, "data:image/png;base64,") {
			t.Fatalf("%v: %+v", call, result)
		}
	}
	for _, target := range []string{secret, outside, filepath.Join(outside, "missing.png")} {
		raw, _ := json.Marshal(map[string]any{"path": target})
		if result := ExecuteResult(ctx, ts, "Image", raw, true); !result.Failed {
			t.Fatalf("%s accepted: %+v", target, result)
		}
	}
	// Outside both the roots and the temp directory nothing changes.
	source, _ := filepath.Abs("liveagent_read.go")
	raw, _ := json.Marshal(map[string]any{"path": source})
	if result := ExecuteResult(ctx, ts, "Image", raw, true); !result.Failed || !strings.Contains(result.Text, "outside the authorized workspace roots") {
		t.Fatalf("file outside roots and temp: %+v", result)
	}
	// Read keeps the workspace roots.
	raw, _ = json.Marshal(map[string]any{"path": secret})
	if result := ExecuteResult(ctx, ts, "Read", raw, true); !result.Failed {
		t.Fatalf("Read outside the roots accepted: %+v", result)
	}
}
