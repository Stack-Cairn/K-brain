package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// flagFixture is a fake provider for kn run flag tests. It records each request
// body and, when writeTarget is set, answers the first request with a write tool
// call for that file.
type flagFixture struct {
	home        string
	mu          sync.Mutex
	bodies      []map[string]any
	writeTarget string
}

func newFlagFixture(t *testing.T, catalog string) *flagFixture {
	t.Helper()
	f := &flagFixture{home: t.TempDir()}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("K_BRAIN_HOME", f.home)
	t.Setenv("LIVEAGENT_HOME", f.home)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(data, &body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		f.mu.Lock()
		f.bodies = append(f.bodies, body)
		first := len(f.bodies) == 1
		f.mu.Unlock()
		delta := map[string]any{"content": "done"}
		if first && f.writeTarget != "" && slices.Contains(toolNames(body), "write") {
			args, _ := json.Marshal(map[string]string{"path": f.writeTarget, "content": "changed"})
			delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "write-1", "type": "function", "function": map[string]any{"name": "write", "arguments": string(args)}}}}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": delta, "finish_reason": "stop"}}})
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
	}))
	t.Cleanup(srv.Close)
	cfg := map[string]any{"defaultModel": "test", "providers": map[string]any{"testprov": map[string]any{
		"baseUrl": srv.URL, "api": "openai-completions", "apiKey": "k",
		"models": []any{map[string]any{"id": "test", "maxTokens": 100}},
	}}}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(f.home, "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if catalog != "" {
		if err := os.WriteFile(filepath.Join(f.home, "models.json"), []byte(catalog), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f *flagFixture) last(t *testing.T) map[string]any {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.bodies) == 0 {
		t.Fatal("no request reached the provider")
	}
	return f.bodies[len(f.bodies)-1]
}

func toolNames(body map[string]any) []string {
	var names []string
	list, _ := body["tools"].([]any)
	for _, raw := range list {
		tool, _ := raw.(map[string]any)
		fn, _ := tool["function"].(map[string]any)
		if name, _ := fn["name"].(string); name != "" {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

func TestRunAllowedToolsLimitsTheOfferedTools(t *testing.T) {
	f := newFlagFixture(t, "")
	if _, err := runCapture(t, "", "-no-session", "-quiet", "-allowed-tools", "read, bash", "hi"); err != nil {
		t.Fatal(err)
	}
	if got := toolNames(f.last(t)); !slices.Equal(got, []string{"bash", "read"}) {
		t.Fatalf("offered tools = %v, want [bash read]", got)
	}
}

func TestRunDisallowedToolsWithholdsTools(t *testing.T) {
	f := newFlagFixture(t, "")
	if _, err := runCapture(t, "", "-no-session", "-quiet", "-disallowed-tools", "bash,write", "hi"); err != nil {
		t.Fatal(err)
	}
	got := toolNames(f.last(t))
	if slices.Contains(got, "bash") || slices.Contains(got, "write") || !slices.Contains(got, "read") {
		t.Fatalf("offered tools = %v, want read without bash and write", got)
	}
}

func TestRunDisallowedToolsCannotRun(t *testing.T) {
	f := newFlagFixture(t, "")
	dir := t.TempDir()
	f.writeTarget = filepath.Join(dir, "marker.txt")
	if _, err := runCapture(t, "", "-no-session", "-quiet", "-disallowed-tools", "write", "write it"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.writeTarget); !os.IsNotExist(err) {
		t.Fatalf("a withheld tool ran: %v", err)
	}
}

func TestRunToolFlagsRejectUnknownNames(t *testing.T) {
	newFlagFixture(t, "")
	for _, flagName := range []string{"-allowed-tools", "-disallowed-tools"} {
		_, err := runCapture(t, "", "-no-session", "-quiet", flagName, "read,nosuchtool", "hi")
		if err == nil || !strings.Contains(err.Error(), `unknown tool "nosuchtool"`) || !strings.Contains(err.Error(), "valid tools:") || !strings.Contains(err.Error(), "read") {
			t.Fatalf("%s: err = %v, want an unknown-tool error listing valid tools", flagName, err)
		}
	}
}

func TestRunAllowAndDenyRules(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		changed bool
	}{
		{"deny without a mode", []string{"-deny", "Write"}, false},
		{"deny beats full access", []string{"-permission-mode", "full_access", "-deny", "Edit(**)"}, false},
		{"deny beats allow", []string{"-allow", "write", "-deny", "Edit(marker.txt)"}, false},
		{"allow in normal mode", []string{"-permission-mode", "normal", "-allow", "Edit(*.txt)"}, true},
		{"allow elsewhere leaves normal mode", []string{"-permission-mode", "normal", "-allow", "Edit(other/**)"}, false},
		{"unrelated deny", []string{"-deny", "Bash(rm:*)"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFlagFixture(t, "")
			// Resolved, so the path matches the working directory path rules see.
			dir, _ := filepath.EvalSymlinks(t.TempDir())
			f.writeTarget = filepath.Join(dir, "marker.txt")
			args := append([]string{"-no-session", "-quiet", "-cwd", dir}, tc.args...)
			if _, err := runCapture(t, "", append(args, "write the marker")...); err != nil {
				t.Fatal(err)
			}
			content, _ := os.ReadFile(f.writeTarget)
			if (string(content) == "changed") != tc.changed {
				t.Fatalf("marker = %q, want changed=%v", content, tc.changed)
			}
			// Run rules are never saved.
			_ = filepath.WalkDir(f.home, func(path string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return nil
				}
				data, _ := os.ReadFile(path)
				if bytes.Contains(data, []byte("Edit(")) || bytes.Contains(data, []byte("Bash(rm")) {
					t.Errorf("%s persisted a run rule", path)
				}
				return nil
			})
		})
	}
}

func TestRunRejectsInvalidRule(t *testing.T) {
	newFlagFixture(t, "")
	for _, flagName := range []string{"-allow", "-deny"} {
		if _, err := runCapture(t, "", "-no-session", "-quiet", flagName, "Bash(", "hi"); err == nil || !strings.Contains(err.Error(), flagName) {
			t.Fatalf("%s: err = %v, want a rule parse error", flagName, err)
		}
	}
}

func TestRunCwdSetsTheWorkingDirectory(t *testing.T) {
	f := newFlagFixture(t, "")
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	before, _ := os.Getwd()
	if _, err := runCapture(t, "", "-quiet", "-cwd", dir, "hi"); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.Getwd(); after != before {
		t.Fatalf("working directory not restored: %s, want %s", after, before)
	}
	st, err := sessionOpen(f.home)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	metas, err := st.Recent(10)
	if err != nil || len(metas) != 1 || metas[0].CWD != dir {
		t.Fatalf("session cwd: %+v, %v; want %s", metas, err, dir)
	}

	for _, bad := range []string{filepath.Join(dir, "missing"), filepath.Join(f.home, "config.json")} {
		if _, err := runCapture(t, "", "-no-session", "-quiet", "-cwd", bad, "hi"); err == nil || !strings.Contains(err.Error(), "-cwd") {
			t.Fatalf("-cwd %s: err = %v", bad, err)
		}
	}
}

func TestRunContinueResumesTheLatestSessionInTheDirectory(t *testing.T) {
	f := newFlagFixture(t, "")
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	other, _ := filepath.EvalSymlinks(t.TempDir())

	if _, err := runCapture(t, "", "-quiet", "-c", "-cwd", dir, "hi"); err == nil || !strings.Contains(err.Error(), "no previous session") {
		t.Fatalf("-c with no session: err = %v", err)
	}
	if _, err := runCapture(t, "", "-quiet", "-cwd", dir, "first prompt here"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCapture(t, "", "-quiet", "-cwd", other, "other directory prompt"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCapture(t, "", "-quiet", "-continue", "-cwd", dir, "second prompt"); err != nil {
		t.Fatal(err)
	}
	msgs, _ := json.Marshal(f.last(t)["messages"])
	if !strings.Contains(string(msgs), "first prompt here") || strings.Contains(string(msgs), "other directory prompt") {
		t.Fatalf("-continue should carry the directory's last session, got %s", msgs)
	}
	st, err := sessionOpen(f.home)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if metas, err := st.Recent(10); err != nil || len(metas) != 2 {
		t.Fatalf("-continue should not create a session: %+v, %v", metas, err)
	}

	for _, args := range [][]string{{"-c", "-resume", "x"}, {"-c", "-no-session"}} {
		if _, err := runCapture(t, "", append(args, "hi")...); err == nil || !strings.Contains(err.Error(), "--continue cannot be combined") {
			t.Fatalf("%v: err = %v", args, err)
		}
	}
}

const visionCatalog = `{"testprov":{"models":[{"id":"test","inputModalities":["text","image"],"reasoningEfforts":["low","medium","high","xhigh"]}]}}`

func writePNG(t *testing.T, path string) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(1, 1, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRunImageAttachesVisionInput(t *testing.T) {
	f := newFlagFixture(t, visionCatalog)
	dir := t.TempDir()
	one, two := filepath.Join(dir, "one.png"), filepath.Join(dir, "two.png")
	writePNG(t, one)
	writePNG(t, two)
	if _, err := runCapture(t, "", "-no-session", "-quiet", "-image", one, "-image", two, "describe"); err != nil {
		t.Fatal(err)
	}
	msgs, _ := json.Marshal(f.last(t)["messages"])
	if got := strings.Count(string(msgs), "data:image/png;base64,"); got != 2 {
		t.Fatalf("want 2 inlined images, got %d in %s", got, msgs)
	}
	if !strings.Contains(string(msgs), "the user attached image(s): "+one+"; "+two) {
		t.Fatalf("missing attachment note: %s", msgs)
	}
}

func TestRunImageRejectsBadInput(t *testing.T) {
	dir := t.TempDir()
	pic := filepath.Join(dir, "pic.png")
	writePNG(t, pic)
	text := filepath.Join(dir, "notes.txt")
	os.WriteFile(text, []byte("x"), 0o600)

	newFlagFixture(t, "")
	if _, err := runCapture(t, "", "-no-session", "-quiet", "-image", pic, "hi"); err == nil || !strings.Contains(err.Error(), "does not accept image input") {
		t.Fatalf("text-only model: err = %v", err)
	}
	newFlagFixture(t, visionCatalog)
	for _, bad := range []string{text, filepath.Join(dir, "missing.png")} {
		if _, err := runCapture(t, "", "-no-session", "-quiet", "-image", bad, "hi"); err == nil || !strings.Contains(err.Error(), "-image") {
			t.Fatalf("-image %s: err = %v", bad, err)
		}
	}
}

func TestRunEffortSetsTheReasoningLevel(t *testing.T) {
	f := newFlagFixture(t, visionCatalog)
	if _, err := runCapture(t, "", "-no-session", "-quiet", "-effort", "xhigh", "hi"); err != nil {
		t.Fatal(err)
	}
	if got := f.last(t)["reasoning_effort"]; got != "xhigh" {
		t.Fatalf("reasoning_effort = %v, want xhigh", got)
	}
	if _, err := runCapture(t, "", "-no-session", "-quiet", "-effort", "off", "hi"); err != nil {
		t.Fatal(err)
	}
	if got, ok := f.last(t)["reasoning_effort"]; ok {
		t.Fatalf("-effort off sent reasoning_effort %v", got)
	}
	_, err := runCapture(t, "", "-no-session", "-quiet", "-effort", "turbo", "hi")
	if err == nil || !strings.Contains(err.Error(), "off, low, medium, high, xhigh") {
		t.Fatalf("unknown level: err = %v", err)
	}
}

func TestRunEffortDefaultsWithoutACatalog(t *testing.T) {
	f := newFlagFixture(t, "")
	if _, err := runCapture(t, "", "-no-session", "-quiet", "-effort", "high", "hi"); err != nil {
		t.Fatal(err)
	}
	if got := f.last(t)["reasoning_effort"]; got != "high" {
		t.Fatalf("reasoning_effort = %v, want high", got)
	}
	if _, err := runCapture(t, "", "-no-session", "-quiet", "-effort", "xhigh", "hi"); err == nil || !strings.Contains(err.Error(), "off, low, medium, high") {
		t.Fatalf("xhigh without a catalog: err = %v", err)
	}
}
