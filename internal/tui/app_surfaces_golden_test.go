package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/mcp"
	"github.com/Stack-Cairn/K-brain/internal/session"
	"github.com/Stack-Cairn/K-brain/internal/skills"
)

// surfaceScenes are the app surfaces drawn by the goldens. Each builds the
// surface on a sized model and returns the screen.
var surfaceScenes = []struct {
	name  string
	build func(t *testing.T, m *model, w, h int) string
}{
	{"help", func(t *testing.T, m *model, _, _ int) string { m.command("/help"); return m.View() }},
	{"settings", func(t *testing.T, m *model, _, _ int) string { m.command("/settings"); return m.View() }},
	{"extensions", func(t *testing.T, m *model, _, _ int) string {
		goldenExtensions(t, m)
		m.command("/extensions")
		return m.View()
	}},
	{"memory", func(t *testing.T, m *model, _, _ int) string {
		dir, err := config.Dir()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		body := "- [ ] prefers pnpm over npm in this monorepo\n- [x] the staging cluster is in eu-west-1\n- [ ] run `make lint` before every commit; CI rejects unformatted Go\n"
		if err := os.WriteFile(filepath.Join(dir, "memory.md"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		m.command("/memory")
		return m.View()
	}},
	{"resume", func(t *testing.T, m *model, _, _ int) string { goldenPicker(m, ""); return m.View() }},
	{"resume-search", func(t *testing.T, m *model, _, _ int) string { goldenPicker(m, "kube"); return m.View() }},
	{"resume-delete", func(t *testing.T, m *model, _, _ int) string {
		goldenPicker(m, "")
		m.picker.idx = 1
		m.picker.confirm = m.picker.metas[1].ID
		return m.View()
	}},
	{"rewind-choice", func(t *testing.T, m *model, _, _ int) string {
		m.rew = &rewindState{
			entries:  []rewindEntry{{cut: 1, text: "refactor the session store so pages load lazily"}},
			choosing: true,
			choice:   2,
		}
		return m.View()
	}},
	{"model-route", func(t *testing.T, m *model, _, _ int) string {
		m.cfg.Providers["long"] = config.Provider{BaseURL: "https://long.example.com/v1", APIKey: "k"}
		m.cfg.Models["glm-5.2-fast"] = config.Model{
			Providers:        []string{"inference", "long"},
			Context:          131072,
			ProviderMetadata: map[string]config.Model{"long": {Context: 1 << 20}},
		}
		m.openModelPicker(false)
		for m.mpicker.view()[m.mpicker.idx].model != "glm-5.2-fast" {
			m.mpicker.idx--
		}
		m.modelPickerKey(tea.KeyMsg{Type: tea.KeyEnter})
		return m.View()
	}},
	{"onboard-trust", func(t *testing.T, _ *model, w, h int) string {
		o := newOnboardModel("/home/dev/src/acme/kn", "/home/dev/.k-brain/config.json", true, true)
		o.Update(mkWinSize(w, h))
		o.choose(0)
		return o.View()
	}},
}

func goldenExtensions(t *testing.T, m *model) {
	off := false
	m.mcpMgr = mcp.NewManager(map[string]mcp.ServerConfig{
		"docs":   {Command: []string{"docs-mcp", "--stdio"}, Enabled: &off},
		"issues": {URL: "https://mcp.example.com/issues", Enabled: &off},
	})
	t.Cleanup(m.mcpMgr.Close)
	m.skillScan = func() []skills.Skill {
		return []skills.Skill{
			{Name: "release", Description: "cut a release: bump the version, write the changelog and tag", Path: "/home/dev/.agents/skills/release/SKILL.md"},
			{Name: "triage", Description: "label and route new issues", Path: "/home/dev/.agents/skills/triage/SKILL.md", DisableModelInvocation: true},
		}
	}
	m.cfg.Hooks = map[string][]config.Hook{
		"PreToolUse": {{Command: "scripts/guard.sh --deny-push", Timeout: 5}},
		"Stop":       {{Command: "notify-send 'kn finished'"}},
	}
}

// goldenPicker opens a session picker on fixed sessions, so the goldens do
// not depend on a store.
func goldenPicker(m *model, filter string) {
	at := time.Now().Add(-2*time.Hour - time.Minute)
	metas := []session.Meta{
		{ID: "a1b2c3d4", Title: "refactor the session store so pages load lazily", Model: "kimi-k3-fast", Provider: "inference", CWD: "/home/dev/src/acme/kn", UpdatedAt: at},
		{ID: "e5f6a7b8", Title: "fix flaky TempDir cleanup on Linux CI", Model: "glm-5.2-fast", Provider: "inference", CWD: "/home/dev/src/acme/kn", Tags: []string{"ci"}, UpdatedAt: at.Add(-24 * time.Hour)},
		{ID: "c9d0e1f2", Title: "", Model: "kimi-k3-fast", Provider: "inference", CWD: "/home/dev/src/acme/api", UpdatedAt: at.Add(-72 * time.Hour)},
	}
	p := &picker{
		all:      metas,
		total:    3,
		previews: map[string][2]string{},
		titles:   map[string]string{"c9d0e1f2": "why does the gateway drop websocket upgrades behind the proxy?"},
		snippets: map[string]string{},
		content:  map[string]bool{},
	}
	p.previews["a1b2c3d4"] = [2]string{"make ListPage load lazily and keep the cursor stable", "Done. ListPage now returns a cursor; the picker loads the next page on scroll."}
	p.previews["e5f6a7b8"] = [2]string{"the TempDir cleanup flakes on Linux", "The watcher still holds the directory open; I close it in t.Cleanup now."}
	p.previews["c9d0e1f2"] = [2]string{"why does the gateway drop websocket upgrades behind the proxy?", "The proxy strips the Upgrade header; forward it explicitly."}
	m.picker = p
	m.sessionID = "a1b2c3d4"
	if filter != "" {
		p.filter, p.hitsFor = filter, filter
		p.hits = []session.Meta{metas[1]}
		p.snippets["e5f6a7b8"] = "assistant: …the kubernetes job mounts the TempDir as a volume"
	}
	m.pickerRebuild()
}

func TestSurfaceGoldens(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("K_BRAIN_HOME", filepath.Join(home, ".k-brain"))
	dir := filepath.Join(home, "src", "acme", "kn")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	defer SetLightTheme(false)

	for _, scene := range surfaceScenes {
		for _, mode := range goldenModes {
			for _, size := range goldenSizes {
				name := fmt.Sprintf("surface-%s_%s_%dx%d", scene.name, mode.name, size[0], size[1])
				t.Run(name, func(t *testing.T) {
					restore := useProfile(t, mode.profile, mode.dark)
					defer restore()
					SetLightTheme(!mode.dark)
					if mode.mono {
						setMonoMode(true, true)
					}
					setASCIIGlyphs(mode.ascii)

					m := compactCmdModel()
					m.permissionMode = "always"
					m.applyAppearance()
					m.Update(mkWinSize(size[0], size[1]))
					view := scene.build(t, m, size[0], size[1])

					lines := strings.Split(view, "\n")
					if len(lines) > size[1] {
						t.Errorf("%d rows on a %d-row screen", len(lines), size[1])
					}
					for i, l := range lines {
						if w := lipgloss.Width(l); w > size[0] {
							t.Errorf("row %d is %d cells wide on a %d-column screen: %q", i, w, size[0], ansi.Strip(l))
						}
					}
					if mode.mono && (strings.Contains(view, "\x1b[38;") || strings.Contains(view, "\x1b[48;")) {
						t.Errorf("no-colour view carries a colour escape")
					}
					if mode.ascii {
						for _, r := range ansi.Strip(view) {
							if r > 0x7e {
								t.Errorf("ascii view contains %q", r)
								break
							}
						}
					}
					checkGolden(t, name+".ansi", view)
					checkGolden(t, name+".txt", stripView(view))
				})
			}
		}
	}
}
