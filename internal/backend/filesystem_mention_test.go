package backend

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func mentionFixture(t *testing.T, files map[string]string) (*Server, string, string) {
	t.Helper()
	root := t.TempDir()
	project := filepath.Join(root, "project")
	for name, body := range files {
		full := filepath.Join(project, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return &Server{filesystemRoots: []string{root}}, root, project
}

func mentionPaths(t *testing.T, server *Server, in filesystemInput) ([]string, bool) {
	t.Helper()
	in.Action = "mention_list"
	result, err := server.filesystem(httptest.NewRequest("POST", "/v1/fs", nil), in)
	if err != nil {
		t.Fatalf("mention_list %+v: %v", in, err)
	}
	out := result.(map[string]any)
	var paths []string
	for _, entry := range out["entries"].([]map[string]string) {
		paths = append(paths, entry["kind"]+":"+entry["path"])
	}
	return paths, out["truncated"].(bool)
}

func TestFilesystemMentionListHonorsIgnores(t *testing.T) {
	server, _, project := mentionFixture(t, map[string]string{
		".gitignore":                  "dist/\n*.log\n/secret.txt\n!keep.log\n",
		"src/app.ts":                  "",
		"src/.gitignore":              "generated.ts\n",
		"src/generated.ts":            "",
		"src/nested/secret.txt":       "", // anchored pattern only covers the root
		"debug.log":                   "",
		"keep.log":                    "",
		"secret.txt":                  "",
		"dist/bundle.js":              "",
		"node_modules/pkg/index.js":   "",
		"web/node_modules/x/index.js": "",
		".git/HEAD":                   "",
		"__pycache__/m.pyc":           "",
		".ignore":                     "tmp\n",
		"tmp/scratch.txt":             "",
		".github/workflows/ci.yml":    "",
	})
	got, truncated := mentionPaths(t, server, filesystemInput{Workdir: project, MaxResults: 100})
	want := []string{"dir:.github", "file:.gitignore", "file:.ignore", "file:keep.log", "dir:src", "dir:web", "dir:.github/workflows", "file:src/.gitignore", "file:src/app.ts", "dir:src/nested", "file:.github/workflows/ci.yml", "file:src/nested/secret.txt"}
	if truncated || !reflect.DeepEqual(got, want) {
		t.Fatalf("mention_list = %v truncated=%v\nwant %v", got, truncated, want)
	}
	// Starting below the root still applies the root's ignore files.
	got, _ = mentionPaths(t, server, filesystemInput{Workdir: project, Path: "src", Query: "gen"})
	if len(got) != 0 { // src/generated.ts is ignored by src/.gitignore
		t.Fatalf("mention_list in src = %v", got)
	}
	got, _ = mentionPaths(t, server, filesystemInput{Workdir: project, Path: "src", Query: "log"})
	if len(got) != 0 { // the root's *.log
		t.Fatalf("mention_list in src = %v", got)
	}
	got, _ = mentionPaths(t, server, filesystemInput{Workdir: project, Path: "dist"})
	if len(got) != 0 {
		t.Fatalf("mention_list in an ignored directory = %v", got)
	}
}

func TestFilesystemMentionListRanksAndCaps(t *testing.T) {
	files := map[string]string{
		"docs/server-notes.md": "",
		"internal/server.go":   "",
		"internal/myserver.go": "",
		"server/handler.go":    "",
		"cmd/s_e_r_v_e_r.go":   "",
		"unrelated/readme.txt": "",
	}
	for i := range 30 {
		files[fmt.Sprintf("bulk/file%02d.txt", i)] = ""
	}
	server, _, project := mentionFixture(t, files)
	got, truncated := mentionPaths(t, server, filesystemInput{Workdir: project, Query: "Server"})
	want := []string{
		"dir:server", "file:internal/server.go", "file:docs/server-notes.md", // name starts with the query
		"file:internal/myserver.go", // name contains it
		"file:server/handler.go",    // path contains it
		"file:cmd/s_e_r_v_e_r.go",   // name subsequence
	}
	if truncated || !reflect.DeepEqual(got, want) {
		t.Fatalf("ranked = %v\nwant %v", got, want)
	}
	got, truncated = mentionPaths(t, server, filesystemInput{Workdir: project, Query: "file", MaxResults: 5})
	if !truncated || len(got) != 5 || got[0] != "file:bulk/file00.txt" {
		t.Fatalf("capped = %v truncated=%v", got, truncated)
	}
	got, _ = mentionPaths(t, server, filesystemInput{Workdir: project, MaxResults: 3})
	if !reflect.DeepEqual(got, []string{"dir:bulk", "dir:cmd", "dir:docs"}) {
		t.Fatalf("empty query = %v", got)
	}
	if _, err := server.filesystem(httptest.NewRequest("POST", "/v1/fs", nil), filesystemInput{Action: "mention_list", Workdir: project, MaxResults: 501}); err == nil {
		t.Fatal("accepted max_results above 500")
	}
}

func TestFilesystemMentionListStaysInWorkspace(t *testing.T) {
	server, root, project := mentionFixture(t, map[string]string{"inside.txt": ""})
	if err := os.WriteFile(filepath.Join(root, "outside.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "elsewhere"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "elsewhere"), filepath.Join(project, "link")); err != nil && runtime.GOOS != "windows" {
		t.Fatal(err)
	}
	got, _ := mentionPaths(t, server, filesystemInput{Workdir: project, Query: "side"})
	if !reflect.DeepEqual(got, []string{"file:inside.txt"}) {
		t.Fatalf("mention_list = %v", got)
	}
	request := httptest.NewRequest("POST", "/v1/fs", nil)
	for _, in := range []filesystemInput{
		{Action: "mention_list", Workdir: project, Path: ".."},
		{Action: "mention_list", Workdir: project, Path: "link"},
		{Action: "mention_list", Workdir: project, Path: "inside.txt"},
		{Action: "mention_list", Workdir: filepath.Dir(root)},
	} {
		if _, err := server.filesystem(request, in); err == nil {
			t.Fatalf("accepted %+v", in)
		}
	}
}

func TestFilesystemMentionListOverHTTP(t *testing.T) {
	server, _, project := mentionFixture(t, map[string]string{"README.md": ""})
	body, _ := json.Marshal(map[string]any{"action": "mention_list", "workdir": project, "query": "read", "max_results": 10})
	recorder := httptest.NewRecorder()
	server.handleFilesystem(recorder, httptest.NewRequest(http.MethodPost, "/v1/fs", bytes.NewReader(body)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body)
	}
	var out struct {
		Path      string              `json:"path"`
		Query     string              `json:"query"`
		Entries   []map[string]string `json:"entries"`
		Truncated *bool               `json:"truncated"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Query != "read" || out.Truncated == nil || *out.Truncated || len(out.Entries) != 1 || out.Entries[0]["path"] != "README.md" || out.Entries[0]["kind"] != "file" {
		t.Fatalf("response = %s", recorder.Body)
	}
}

func TestFilesystemMentionListStopsAtScanLimit(t *testing.T) {
	files := map[string]string{}
	for i := range 20 {
		files[fmt.Sprintf("f%02d.txt", i)] = ""
	}
	server, _, project := mentionFixture(t, files)
	saved := mentionScanLimit
	mentionScanLimit = 5
	t.Cleanup(func() { mentionScanLimit = saved })
	got, truncated := mentionPaths(t, server, filesystemInput{Workdir: project, MaxResults: 100})
	if !truncated || len(got) != 5 {
		t.Fatalf("scan-limited = %v truncated=%v", got, truncated)
	}
}
