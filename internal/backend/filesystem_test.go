package backend

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFilesystemRootConfinementAndText(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"project/readme.txt": "hello\nworld", "outside.txt": "private", "project/.hidden": "hidden", "project/binary": "\x00binary"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Creating a symlink on Windows needs a privilege most accounts lack. The other cases still
	// apply, so only the ones that need the link drop out.
	listed := uint32(3)
	escapes := []filesystemInput{
		{Action: "read_text", Workdir: project, Path: "escape"},
		{Action: "read_preview", Workdir: project, Path: "escape"},
	}
	if err := os.Symlink("../outside.txt", filepath.Join(project, "escape")); err != nil {
		if runtime.GOOS != "windows" {
			t.Fatal(err)
		}
		t.Logf("not testing symlink escapes: %v", err)
		listed, escapes = 2, nil
	}
	server := &Server{filesystemRoots: []string{root}}
	request := httptest.NewRequest("POST", "/v1/fs", nil)
	result, err := server.filesystem(request, filesystemInput{Action: "read_text", Workdir: project, Path: "readme.txt"})
	if err != nil {
		t.Fatal(err)
	}
	text := result.(map[string]any)
	if text["content"] != "hello\nworld" || text["total_lines"] != 2 || len(text["content_hash"].(string)) != 64 {
		t.Fatal(text)
	}
	for _, input := range append(escapes, []filesystemInput{
		{Action: "read_text", Workdir: project, Path: "../outside.txt"},
		{Action: "read_preview", Workdir: project, Path: "../outside.txt"},
		{Action: "read_text", Workdir: project, Path: "binary"},
		{Action: "read_text", Workdir: project, Path: root},
		{Action: "list_dirs", Path: filepath.Dir(root)},
		{Action: "list", Workdir: project, Depth: 100},
		{Action: "unsupported", Workdir: project, Path: "readme.txt"},
	}...) {
		if _, err := server.filesystem(request, input); err == nil {
			t.Fatalf("accepted invalid operation: %+v", input)
		}
	}
	result, err = server.filesystem(request, filesystemInput{Action: "list", Workdir: project, MaxResults: 1})
	if err != nil {
		t.Fatal(err)
	}
	listing := result.(map[string]any)
	if listing["total"] != listed || listing["has_more"] != true || len(listing["entries"].([]map[string]any)) != 1 {
		t.Fatal(listing)
	}
	result, err = server.filesystem(request, filesystemInput{Action: "list_dirs", Path: root})
	if err != nil || !strings.Contains(result.(map[string]any)["entries"].([]map[string]string)[0]["path"], "project") {
		t.Fatalf("directories=%v err=%v", result, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := server.filesystem(request.WithContext(ctx), filesystemInput{Action: "list", Workdir: project}); err == nil {
		t.Fatal("cancelled traversal continued")
	}
}

func TestFilesystemHTTPRequiresAuthentication(t *testing.T) {
	server := &Server{token: "secret"}
	request := httptest.NewRequest("POST", "/v1/fs", strings.NewReader(`{"action":"roots"}`))
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != 401 {
		t.Fatalf("unauthorized filesystem request: %d", response.Code)
	}
	request.Header.Set("Authorization", "Bearer secret")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"roots":[]`) {
		t.Fatalf("authorized roots: %d %s", response.Code, response.Body.String())
	}
}
