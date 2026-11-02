package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// symlinkOrSkip creates a symlink, or skips the test where the platform forbids it
// (Windows requires SeCreateSymbolicLinkPrivilege).
func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation requires privileges on Windows")
		}
		t.Fatal(err)
	}
}

func TestLiveAgentCatalogMatchesOriginalNamesAndSchemas(t *testing.T) {
	catalog := LiveAgentCatalog()
	want := []string{"TerminalSession", "ReadTerminal", "Read", "Image", "Write", "Edit", "Delete", "List", "Glob", "Grep", "Bash", "ManagedProcess", "ProcessWait", "ProcessStop"}
	if len(catalog) != len(want) {
		t.Fatalf("got %d tools, want %d", len(catalog), len(want))
	}
	for i, tool := range catalog {
		if tool.Def.Function.Name != want[i] {
			t.Errorf("tool %d = %q, want %q", i, tool.Def.Function.Name, want[i])
		}
		var schema map[string]any
		if err := json.Unmarshal(tool.Def.Function.Parameters, &schema); err != nil {
			t.Fatalf("%s schema: %v", want[i], err)
		}
		if schema["type"] != "object" || schema["additionalProperties"] != false {
			t.Errorf("%s schema is not strict object: %s", want[i], tool.Def.Function.Parameters)
		}
	}
	if string(catalog[0].Def.Function.Parameters) == string(All()[1].Def.Function.Parameters) {
		t.Fatal("PascalCase Read was incorrectly aliased to lowercase read")
	}
}

func TestLiveAgentWorkspaceAndMutationPolicy(t *testing.T) {
	dir := t.TempDir()
	ctx := WithWorkspaceRoots(WithWorkingDir(context.Background(), dir), []WorkspaceRoot{{Path: dir, Access: "write"}})
	var paths []string
	ctx = WithFileMutationObserver(ctx, func(path string) { paths = append(paths, path) })
	write := findToolForTest(LiveAgentCatalog(), "Write")
	args := json.RawMessage(`{"path":"nested/file.txt","content":"hello"}`)
	if _, err := write.Run(ctx, args); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != filepath.Join(dir, "nested", "file.txt") {
		t.Fatalf("mutation paths = %#v", paths)
	}
	if _, err := write.Run(ctx, json.RawMessage(`{"path":"../escape.txt","content":"no"}`)); err == nil {
		t.Fatal("path traversal was accepted")
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, outside, filepath.Join(dir, "link"))
	if _, err := write.Run(ctx, json.RawMessage(`{"path":"link","content":"no"}`)); err == nil {
		t.Fatal("symlink write was accepted")
	}
}

func TestLiveAgentEditExpectedReplacementsAndDelete(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("x x"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := WithWorkspaceRoots(WithWorkingDir(context.Background(), dir), []WorkspaceRoot{{Path: dir, Access: "write"}})
	edit := findToolForTest(LiveAgentCatalog(), "Edit")
	if _, err := edit.Run(ctx, json.RawMessage(`{"path":"a.txt","old_string":"x","new_string":"y","expected_replacements":1}`)); err == nil {
		t.Fatal("expected replacement count failure")
	}
	if _, err := edit.Run(ctx, json.RawMessage(`{"path":"a.txt","old_string":"x","new_string":"y","replace_all":true,"expected_replacements":2}`)); err != nil {
		t.Fatal(err)
	}
	deleteTool := findToolForTest(LiveAgentCatalog(), "Delete")
	if _, err := deleteTool.Run(ctx, json.RawMessage(`{"path":"a.txt"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file remains: %v", err)
	}
}

func TestLiveAgentGrepAndCancellation(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(WithWorkingDir(context.Background(), dir))
	cancel()
	grep := findToolForTest(LiveAgentCatalog(), "Grep")
	if _, err := grep.Run(ctx, json.RawMessage(`{"pattern":"needle"}`)); err == nil {
		t.Fatal("cancelled grep succeeded")
	}
	ctx = WithWorkspaceRoots(WithWorkingDir(context.Background(), dir), []WorkspaceRoot{{Path: dir, Access: "read"}})
	out, err := grep.Run(ctx, json.RawMessage(`{"pattern":"needle","file_pattern":"*.go"}`))
	if err != nil || !strings.Contains(out, "needle") {
		t.Fatalf("grep = %q, %v", out, err)
	}
}

func TestLiveAgentOriginalSchemaCoverage(t *testing.T) {
	expected := map[string]string{
		"TerminalSession": "command",
		"ReadTerminal":    "session_id,max_bytes",
		"Read":            "path,start_line,limit,page_start,page_limit,cell_start,cell_limit",
		"Image":           "path,paths,url,urls,base64,base64s,mimeType,source,sources",
		"Write":           "path,content", "Edit": "path,old_string,new_string,expected_replacements,replace_all",
		"Delete": "path", "List": "path,depth,offset,max_results", "Glob": "pattern,path,offset,max_results,sort_by",
		"Grep": "pattern,path,file_pattern,ignore_case,output_mode,head_limit,offset,context,multiline", "Bash": "command,cwd,shell,timeout_ms,yield_time_ms",
		"ManagedProcess": "action,command,cwd,shell,label,isolated,process_id,cursor,yield_time_ms,max_bytes", "ProcessWait": "session_id,cursor,yield_time_ms", "ProcessStop": "session_id,cursor",
	}
	required := map[string]string{"TerminalSession": "command", "ReadTerminal": "session_id", "Read": "path", "Write": "path,content", "Edit": "path,old_string,new_string", "Delete": "path", "Glob": "pattern", "Grep": "pattern", "Bash": "command", "ManagedProcess": "action", "ProcessWait": "session_id", "ProcessStop": "session_id"}
	metadata := LiveAgentToolCatalogMetadata()
	for i, tool := range LiveAgentCatalog() {
		name := tool.Def.Function.Name
		var schema struct {
			Properties map[string]map[string]any `json:"properties"`
			Required   []string                  `json:"required"`
		}
		if err := json.Unmarshal(tool.Def.Function.Parameters, &schema); err != nil {
			t.Fatal(err)
		}
		if len(schema.Properties) != len(strings.Split(expected[name], ",")) {
			t.Fatalf("%s properties=%v", name, schema.Properties)
		}
		for _, key := range strings.Split(expected[name], ",") {
			p, ok := schema.Properties[key]
			if !ok {
				t.Errorf("%s missing %s", name, key)
			}
			if p["type"] == nil && p["anyOf"] == nil {
				t.Errorf("%s.%s missing type", name, key)
			}
			if key != "output_mode" && p["description"] == nil {
				t.Errorf("%s.%s missing original description", name, key)
			}
		}
		if strings.Join(schema.Required, ",") != required[name] {
			t.Errorf("%s required=%v", name, schema.Required)
		}
		m := metadata[i]
		if m.ToolName != name || m.ID != strings.ToLower(name) || m.Icon == "" || m.DefaultPolicy != "allow" || len(m.RuntimeScope) != 2 {
			t.Errorf("metadata=%+v", m)
		}
		if m.ReadOnly != (name == "Read" || name == "Image" || name == "List" || name == "Glob" || name == "Grep" || name == "ReadTerminal") {
			t.Errorf("read-only metadata: %+v", m)
		}
	}
}

func TestLiveAgentPolicyDenialAndReadOnlyRoots(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{"TerminalSession": `{"command":"echo bad"}`, "ReadTerminal": `{"session_id":"missing"}`, "Read": `{"path":"a.txt"}`, "Image": `{"path":"a.txt"}`, "Write": `{"path":"a.txt","content":"bad"}`, "Edit": `{"path":"a.txt","old_string":"hello","new_string":"bad"}`, "Delete": `{"path":"a.txt"}`, "List": `{}`, "Glob": `{"pattern":"**/*"}`, "Grep": `{"pattern":"hello"}`, "Bash": `{"command":"echo bad"}`, "ManagedProcess": `{"action":"start","command":"echo bad"}`, "ProcessWait": `{"session_id":"missing"}`, "ProcessStop": `{"session_id":"missing"}`}
	catalog := LiveAgentCatalog()
	for _, tool := range catalog {
		t.Run(tool.Def.Function.Name, func(t *testing.T) {
			name := tool.Def.Function.Name
			calls := 0
			mutations := 0
			ctx := WithWorkspaceRoots(WithWorkingDir(context.Background(), dir), []WorkspaceRoot{{Path: dir, Access: "write"}})
			ctx = WithFileMutationObserver(ctx, func(string) { mutations++ })
			ctx = WithGate(ctx, func(req GateRequest) (GateDecision, string) {
				calls++
				if req.Tool != name {
					t.Errorf("policy name=%q want %q", req.Tool, name)
				}
				return GateReject, "test denial"
			})
			args := json.RawMessage(cases[name])
			var cleanup func()
			if name == "ProcessWait" || name == "ProcessStop" {
				startCtx := WithWorkspaceRoots(WithWorkingDir(context.Background(), dir), []WorkspaceRoot{{Path: dir, Access: "write"}})
				start := findToolForTest(catalog, "ManagedProcess")
				started, err := start.Run(startCtx, json.RawMessage(`{"action":"start","command":"sleep 30"}`))
				if err != nil {
					t.Fatal(err)
				}
				id := fieldValue(started, "process_id")
				if id == "" {
					t.Fatalf("missing process id: %q", started)
				}
				args = mustJSON(map[string]any{"session_id": id})
				cleanup = func() {
					_, _ = findToolForTest(catalog, "ManagedProcess").Run(startCtx, mustJSON(map[string]any{"action": "stop", "process_id": id}))
				}
				defer cleanup()
			}
			result := ExecuteResult(ctx, []Tool{tool}, name, args, true)
			if !result.Failed || calls != 1 || mutations != 0 {
				t.Fatalf("denial result=%+v calls=%d mutations=%d", result, calls, mutations)
			}
			if name == "Write" || name == "Edit" || name == "Delete" || name == "Bash" || name == "ManagedProcess" {
				ctx = WithWorkspaceRoots(WithWorkingDir(context.Background(), dir), []WorkspaceRoot{{Path: dir, Access: "read"}})
				if _, err := tool.Run(ctx, json.RawMessage(cases[name])); err == nil {
					t.Fatal("read-only root allowed mutation")
				}
			}
		})
	}
}

func TestLiveAgentStaleReadAndLenientEdit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := WithWorkingDir(context.Background(), dir)
	ts := LiveAgentCatalog()
	read := findToolForTest(ts, "Read")
	edit := findToolForTest(ts, "Edit")
	if _, err := read.Run(ctx, json.RawMessage(`{"path":"a.txt"}`)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("external\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := edit.Run(ctx, json.RawMessage(`{"path":"a.txt","old_string":"external","new_string":"bad"}`)); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("stale edit=%v", err)
	}
	for _, tc := range []struct{ text, old, new, want, strategy string }{
		{"a\r\nb\r\n", "a\nb", "c\nd", "c\r\nd\r\n", "line-endings"},
		{"a  \nb\n", "a\nb", "c", "c\n", "trailing-whitespace"},
		{"    one\n    two\n", "one\ntwo", "three\nfour", "    three\n    four\n", "indentation"},
	} {
		if err := os.WriteFile(path, []byte(tc.text), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := read.Run(ctx, json.RawMessage(`{"path":"a.txt"}`)); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(map[string]any{"path": "a.txt", "old_string": tc.old, "new_string": tc.new})
		out, err := edit.Run(ctx, raw)
		if err != nil || !strings.Contains(out, tc.strategy) {
			t.Fatalf("%s edit=%q %v", tc.strategy, out, err)
		}
		got, _ := os.ReadFile(path)
		if string(got) != tc.want {
			t.Fatalf("%s got %q want %q", tc.strategy, got, tc.want)
		}
	}
}

func TestLiveAgentStrictArgumentsAndSearchOptions(t *testing.T) {
	ctx := WithWorkingDir(context.Background(), t.TempDir())
	ts := LiveAgentCatalog()
	for _, tc := range []struct{ name, args string }{{"Read", `{"path":"a","offset":1}`}, {"Bash", `{"command":"echo x","timeout":1}`}, {"Write", `{"path":"a"}`}, {"List", `{"offset":-1}`}, {"Grep", `{"pattern":"x","ignore_case":"false"}`}, {"Glob", `{"pattern":"x","sort_by":"mtime"}`}, {"Image", `{"paths":[]}`}} {
		if result := ExecuteResult(ctx, ts, tc.name, json.RawMessage(tc.args), true); !result.Failed {
			t.Errorf("invalid %s succeeded: %+v", tc.name, result)
		}
	}
	dir := WorkingDir(ctx)
	for path, text := range map[string]string{"src/a.go": "Before\nNEEDLE\nafter\n", "src/b.ts": "needle\nnext", "ignore.txt": "needle", ".ignore": "ignore.txt\n"} {
		p := filepath.Join(dir, path)
		_ = os.MkdirAll(filepath.Dir(p), 0755)
		if err := os.WriteFile(p, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct{ name, args, want string }{{"Glob", `{"pattern":"**/*.{go,ts}","offset":1,"max_results":1}`, "src/b.ts"}, {"Grep", `{"pattern":"needle\\nnext","multiline":true,"file_pattern":"**/*.ts"}`, "src/b.ts:2: next"}, {"Grep", `{"pattern":"needle","context":1,"file_pattern":"**/*.go"}`, "src/a.go:1: Before"}, {"Grep", `{"pattern":"needle","output_mode":"count"}`, "matches=2"}} {
		out, err := findToolForTest(ts, tc.name).Run(ctx, json.RawMessage(tc.args))
		if err != nil || !strings.Contains(out, tc.want) || strings.Contains(out, "ignore.txt") {
			t.Fatalf("%s=%q %v", tc.name, out, err)
		}
	}
}

func TestLiveAgentNamedRootsAndDeleteCheckpoint(t *testing.T) {
	dir := t.TempDir()
	other := t.TempDir()
	ctx := WithWorkingDir(context.Background(), dir)
	ctx = WithLiveAgentNamedRoots(ctx, map[string]WorkspaceRoot{"root://other": {Path: other, Access: "write"}})
	ts := LiveAgentCatalog()
	if _, err := findToolForTest(ts, "Write").Run(ctx, json.RawMessage(`{"path":"root://other/nested/a","content":"hello","mode":"rewrite"}`)); err != nil {
		t.Fatal(err)
	}
	var captured []string
	ctx = WithFileMutationObserver(ctx, func(p string) { captured = append(captured, p) })
	if _, err := findToolForTest(ts, "Delete").Run(ctx, json.RawMessage(`{"path":"root://other/nested"}`)); err != nil {
		t.Fatal(err)
	}
	if len(captured) != 2 {
		t.Fatalf("directory mutation preimages=%v", captured)
	}
	if _, err := findToolForTest(ts, "Write").Run(ctx, json.RawMessage(`{"path":"root://other/../escape","content":"bad"}`)); err == nil {
		t.Fatal("named root traversal allowed")
	}
}

func TestLiveAgentReadOnlyAndSymlinkSearchBoundaries(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	_ = os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0600)
	symlinkOrSkip(t, outside, filepath.Join(root, "link"))
	ts := LiveAgentCatalog()
	ctx := WithWorkspaceRoots(WithWorkingDir(context.Background(), root), []WorkspaceRoot{{Path: root, Access: "write"}})
	for _, tc := range []struct{ name, args string }{{"Read", `{"path":"link/secret.txt"}`}, {"List", `{"path":"link"}`}, {"Glob", `{"path":"link","pattern":"**/*"}`}, {"Grep", `{"path":"link","pattern":"secret"}`}, {"Delete", `{"path":"link"}`}, {"Bash", `{"command":"pwd","cwd":"link"}`}, {"Delete", `{"path":"."}`}} {
		if result := ExecuteResult(ctx, ts, tc.name, json.RawMessage(tc.args), true); !result.Failed {
			t.Errorf("boundary bypass: %s %s: %+v", tc.name, tc.args, result)
		}
	}
	empty := WithWorkspaceRoots(ctx, nil)
	if result := ExecuteResult(empty, ts, "List", json.RawMessage(`{}`), false); !result.Failed {
		t.Fatal("explicit empty roots were broadened")
	}
}

func TestLiveAgentImagesAndReadWindows(t *testing.T) {
	dir := t.TempDir()
	ctx := WithWorkingDir(context.Background(), dir)
	ts := LiveAgentCatalog()
	image := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Wl6WQAAAABJRU5ErkJggg=="
	raw, _ := json.Marshal(map[string]any{"sources": []string{"data:image/png;base64," + image, "https://example.test/picture.png", "<svg xmlns=\"http://www.w3.org/2000/svg\" width=\"1\" height=\"1\"/>"}})
	result := ExecuteResult(ctx, ts, "Image", raw, true)
	if result.Failed || len(result.Parts) != 3 {
		t.Fatalf("image result=%+v", result)
	}
	if !strings.HasPrefix(result.Parts[0].ImageURL.URL, "data:image/png;base64,") || result.Parts[1].ImageURL.URL != "https://example.test/picture.png" {
		t.Fatalf("image parts=%+v", result.Parts)
	}
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\nthree"), 0600)
	read := findToolForTest(ts, "Read")
	out, err := read.Run(ctx, json.RawMessage(`{"path":"a.txt","start_line":2,"limit":1}`))
	if err != nil || !strings.Contains(out, "2\ttwo") || strings.Contains(out, "3\tthree") {
		t.Fatalf("text window=%q %v", out, err)
	}
	out, err = read.Run(ctx, json.RawMessage(`{"path":"a.txt","start_line":2,"limit":1}`))
	if err != nil || !strings.Contains(out, "unchanged") {
		t.Fatalf("unchanged=%q %v", out, err)
	}
	_ = os.WriteFile(filepath.Join(dir, "a.ipynb"), []byte(`{"cells":[{"cell_type":"markdown","source":["first"]},{"cell_type":"code","source":["second"]}]}`), 0600)
	out, err = read.Run(ctx, json.RawMessage(`{"path":"a.ipynb","cell_start":2,"cell_limit":1}`))
	if err != nil || !strings.Contains(out, "second") || strings.Contains(out, "first") {
		t.Fatalf("cell window=%q %v", out, err)
	}
}

// Strict-schema models fill every Image field: blanks for unused ones and the
// same local path repeated in paths, sources, urls and base64s (recorded calls).
func TestLiveAgentImageToleratesStrictSchemaFilledFields(t *testing.T) {
	dir := t.TempDir()
	ctx := WithWorkingDir(context.Background(), dir)
	ts := LiveAgentCatalog()
	png, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Wl6WQAAAABJRU5ErkJggg==")
	if err := os.MkdirAll(filepath.Join(dir, ".debug"), 0o700); err != nil {
		t.Fatal(err)
	}
	local := ".debug/quote-desktop.png"
	if err := os.WriteFile(filepath.Join(dir, local), png, 0o600); err != nil {
		t.Fatal(err)
	}
	absolute := filepath.Join(dir, local)
	for name, call := range map[string]map[string]any{
		"blank placeholders":     {"base64": "", "base64s": []string{""}, "mimeType": "", "path": local, "paths": []string{local}, "source": "", "sources": []string{local}, "url": "", "urls": []string{""}},
		"path in every field":    {"base64": "", "base64s": []string{local}, "mimeType": "", "path": local, "paths": []string{local}, "source": local, "sources": []string{local}, "url": "", "urls": []string{local}},
		"windows path as base64": {"base64": absolute, "path": "", "paths": []string{""}, "url": "", "urls": []string{""}},
		"file url as url":        {"url": "file:///" + filepath.ToSlash(absolute)},
	} {
		raw, _ := json.Marshal(call)
		result := ExecuteResult(ctx, ts, "Image", raw, true)
		if result.Failed {
			t.Fatalf("%s: failed: %s", name, result.Text)
		}
		if name == "file url as url" {
			if len(result.Parts) != 1 {
				t.Fatalf("%s: parts=%d", name, len(result.Parts))
			}
			continue
		}
		if len(result.Parts) != 1 {
			t.Fatalf("%s: duplicate sources not collapsed, parts=%d text=%q", name, len(result.Parts), result.Text)
		}
		if !strings.HasPrefix(result.Parts[0].ImageURL.URL, "data:image/png;base64,") {
			t.Fatalf("%s: part=%+v", name, result.Parts[0])
		}
	}
	for name, call := range map[string]map[string]any{
		"only blanks":         {"path": "", "paths": []string{""}, "urls": []string{"  "}},
		"credential url":      {"url": "https://user:pass@example.test/a.png"},
		"non-http scheme":     {"urls": []string{"ftp://example.test/a.png"}},
		"bad base64 data url": {"base64": "data:image/png;base64,***"},
	} {
		raw, _ := json.Marshal(call)
		if result := ExecuteResult(ctx, ts, "Image", raw, true); !result.Failed {
			t.Fatalf("%s: accepted: %+v", name, result)
		}
	}
}

func TestLiveAgentBashStructuredFailuresAndBackgroundPolicy(t *testing.T) {
	ctx := WithWorkingDir(context.Background(), t.TempDir())
	ts := LiveAgentCatalog()
	result := ExecuteResult(ctx, ts, "Bash", json.RawMessage(`{"command":"exit 7"}`), false)
	if !result.Failed {
		t.Fatalf("nonzero exit was success: %+v", result)
	}
	result = ExecuteResult(ctx, ts, "Bash", json.RawMessage(`{"command":"sleep 5","timeout_ms":1000}`), false)
	if !result.Failed || !strings.Contains(result.Text, "timed out") {
		t.Fatalf("timeout result=%+v", result)
	}
	for _, command := range []string{"server &", "server > log &"} {
		if err := liveagentValidateBackground(command); err == nil {
			t.Errorf("unsafe background accepted: %s", command)
		}
	}
	for _, command := range []string{"echo 'a & b'", "true && true", "server > log 2>&1 &"} {
		if err := liveagentValidateBackground(command); err != nil {
			t.Errorf("safe syntax rejected: %s: %v", command, err)
		}
	}
}
