package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/datapath"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
	"github.com/Stack-Cairn/K-brain/internal/skills"
)

func TestSkillsHTTPScanExternal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(datapath.LiveAgentHomeEnv, t.TempDir())
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	client := &scriptedClient{response: "ok"}
	s, err := New(Options{Store: store, Factory: func(_ context.Context, _ string, m protocol.ModelRef) (*agent.Agent, error) {
		return agent.New(client, m.Model, 100, "system"), nil
	}, EventDir: t.TempDir(), Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	do := func(method, path string, body any, authorized bool) *httptest.ResponseRecorder {
		t.Helper()
		var payload string
		if body != nil {
			b, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			payload = string(b)
		}
		r := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(payload))
		if authorized {
			req.Header.Set("Authorization", "Bearer secret")
		}
		req.Header.Set("Content-Type", "application/json")
		s.ServeHTTP(r, req)
		return r
	}
	scanRequest := map[string]any{"action": "scan_external"}
	if r := do(http.MethodPost, "/v1/skills/manage", scanRequest, false); r.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized scan %d: %s", r.Code, r.Body.String())
	}

	fixtures := []struct {
		tool, root, name string
	}{
		{"gemini", ".gemini/skills", "gemini-fixture"},
		{"gemini", ".gemini/config/skills", "antigravity-config"},
		{"gemini", ".gemini/antigravity-cli/skills", "antigravity-cli"},
		{"gemini", ".gemini/antigravity/skills", "antigravity-fixture"},
	}
	sources := map[string]string{}
	for _, fixture := range fixtures {
		dir := filepath.Join(home, filepath.FromSlash(fixture.root), fixture.name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		content := "---\nname: " + fixture.name + "\ndescription: HTTP external fixture\n---\nSource instructions for " + fixture.name + "\n"
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		sources[dir] = content
	}

	r := do(http.MethodPost, "/v1/skills/manage", scanRequest, true)
	if r.Code != http.StatusOK {
		t.Fatalf("scan %d: %s", r.Code, r.Body.String())
	}
	var response skills.ManagerResponse
	if err := json.Unmarshal(r.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Action != "scan_external" || len(response.External) != 4 {
		t.Fatalf("unexpected scan response: %s", r.Body.String())
	}
	scans := map[string]skills.ExternalScan{}
	for _, scan := range response.External {
		if _, duplicate := scans[scan.Tool]; duplicate {
			t.Fatalf("duplicate tool %q", scan.Tool)
		}
		if scan.Skills == nil || scan.Errors == nil || len(scan.Errors) != 0 {
			t.Fatalf("expected non-null arrays and no errors: %+v", scan)
		}
		scans[scan.Tool] = scan
	}
	for _, tool := range []string{"agents", "codex", "claude-code", "gemini"} {
		scan, ok := scans[tool]
		if !ok {
			t.Fatalf("missing source %q: %+v", tool, scans)
		}
		wantSkills := 0
		if tool == "gemini" {
			wantSkills = len(fixtures)
		}
		if scan.Exists != (wantSkills > 0) || len(scan.Skills) != wantSkills {
			t.Fatalf("unexpected source state: %+v", scan)
		}
	}

	for _, fixture := range fixtures {
		src := filepath.Join(home, filepath.FromSlash(fixture.root), fixture.name)
		var entry skills.ExternalSkill
		for _, skill := range scans[fixture.tool].Skills {
			if skill.BaseDir == src {
				entry = skill
			}
		}
		if entry.Name != fixture.name || entry.SkillFile != filepath.Join(src, "SKILL.md") {
			t.Fatalf("missing or invalid source metadata for %s: %+v", src, entry)
		}
		r = do(http.MethodPost, "/v1/skills/manage", map[string]any{
			"action": "install", "source": entry.BaseDir, "conflict": "fail",
		}, true)
		if r.Code != http.StatusOK {
			t.Fatalf("install %s %d: %s", fixture.name, r.Code, r.Body.String())
		}
		r = do(http.MethodGet, "/v1/skills/file?path="+url.QueryEscape(fixture.name+"/SKILL.md"), nil, true)
		if r.Code != http.StatusOK {
			t.Fatalf("read installed %s %d: %s", fixture.name, r.Code, r.Body.String())
		}
		var file struct {
			Content string `json:"content"`
		}
		if err := json.Unmarshal(r.Body.Bytes(), &file); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(file.Content, "Source instructions for "+fixture.name) {
			t.Fatalf("unexpected installed content: %q", file.Content)
		}
		after, err := os.ReadFile(entry.SkillFile)
		if err != nil || string(after) != sources[src] {
			t.Fatalf("source changed after import %s: %v", src, err)
		}
	}
}
