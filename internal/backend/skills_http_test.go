package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/datapath"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func TestSkillsHTTPFixture(t *testing.T) {
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
	r := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/skills", nil)
	req.Header.Set("Authorization", "Bearer secret")
	s.ServeHTTP(r, req)
	if r.Code != 200 {
		t.Fatalf("list status %d: %s", r.Code, r.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["rootDir"] == nil {
		t.Fatal("missing root")
	}
	payload := `{"action":"create","name":"http-fixture","description":"fixture","body":"workflow"}`
	r = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/skills/manage", strings.NewReader(payload))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	s.ServeHTTP(r, req)
	if r.Code != 200 {
		t.Fatalf("create status %d: %s", r.Code, r.Body.String())
	}
	r = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/skills/file?path=http-fixture/SKILL.md", nil)
	req.Header.Set("Authorization", "Bearer secret")
	s.ServeHTTP(r, req)
	if r.Code != 200 {
		t.Fatalf("read status %d: %s", r.Code, r.Body.String())
	}
}

func TestSkillsHTTPSkillPacks(t *testing.T) {
	home := t.TempDir()
	t.Setenv(datapath.LiveAgentHomeEnv, home)
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(home, "skills", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("demo-pack/skills/SKILL.md", "---\nname: demo-router\ndescription: Router\n---\n")
	write("demo-pack/skills/radare2/SKILL.md", "---\nname: radare2\ndescription: r2\n---\n")
	write("demo-pack/skills/radare2/references/commands.md", "r2 commands\n")
	write("demo-pack/plugins/adapter/skills/demo-router/SKILL.md", "---\nname: demo-router\ndescription: copy\n---\n")
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
	do := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer secret")
		req.Header.Set("Content-Type", "application/json")
		s.ServeHTTP(r, req)
		return r
	}
	type item struct {
		Name       string `json:"name"`
		SkillFile  string `json:"skillFile"`
		BaseDir    string `json:"baseDir"`
		Pack       string `json:"pack"`
		Shadowed   bool   `json:"shadowed"`
		ShadowedBy string `json:"shadowedBy"`
	}
	var list struct {
		Skills   []item `json:"skills"`
		Shadowed []item `json:"shadowed"`
		Settings struct {
			Packs map[string]bool `json:"packs"`
		} `json:"settings"`
	}
	r := do(http.MethodGet, "/v1/skills", "")
	if r.Code != 200 {
		t.Fatalf("list status %d: %s", r.Code, r.Body.String())
	}
	if err := json.Unmarshal(r.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	var r2 *item
	for i := range list.Skills {
		if list.Skills[i].Name == "radare2" {
			r2 = &list.Skills[i]
		}
	}
	if r2 == nil || r2.Pack != "demo-pack" || r2.BaseDir != "demo-pack/skills/radare2" || r2.SkillFile != "demo-pack/skills/radare2/SKILL.md" || r2.Shadowed {
		t.Fatalf("radare2 item = %+v (%s)", r2, r.Body.String())
	}
	if len(list.Shadowed) != 1 || list.Shadowed[0].BaseDir != "demo-pack/plugins/adapter/skills/demo-router" || !list.Shadowed[0].Shadowed || list.Shadowed[0].ShadowedBy != "demo-pack/skills" {
		t.Fatalf("shadowed = %+v", list.Shadowed)
	}
	if r = do(http.MethodGet, "/v1/skills/file?path=demo-pack/skills/radare2/references/commands.md", ""); r.Code != 200 || !strings.Contains(r.Body.String(), "r2 commands") {
		t.Fatalf("read pack file %d: %s", r.Code, r.Body.String())
	}
	if r = do(http.MethodPut, "/v1/skills/settings", `{"enabled":true,"selected":["radare2"],"packs":{"demo-pack":false}}`); r.Code != 200 {
		t.Fatalf("save settings %d: %s", r.Code, r.Body.String())
	}
	// A client without packs keeps the stored switches.
	if r = do(http.MethodPut, "/v1/skills/settings", `{"enabled":true,"selected":["radare2"]}`); r.Code != 200 {
		t.Fatalf("save settings %d: %s", r.Code, r.Body.String())
	}
	r = do(http.MethodGet, "/v1/skills", "")
	list.Settings.Packs = nil
	if err := json.Unmarshal(r.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if v, ok := list.Settings.Packs["demo-pack"]; !ok || v {
		t.Fatalf("settings.packs = %v", list.Settings.Packs)
	}
	if r = do(http.MethodPost, "/v1/skills/manage", `{"action":"delete","name":"radare2"}`); r.Code == 200 || !strings.Contains(r.Body.String(), "skill pack") {
		t.Fatalf("delete pack skill %d: %s", r.Code, r.Body.String())
	}
}
