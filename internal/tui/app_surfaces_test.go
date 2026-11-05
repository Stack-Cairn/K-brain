package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/mcp"
	"github.com/Stack-Cairn/K-brain/internal/skills"
)

func surfaceModel(t *testing.T) *model {
	t.Helper()
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	m := compactCmdModel()
	m.Update(mkWinSize(80, 24))
	return m
}

func pressAll(t *testing.T, m *model, keys ...tea.KeyMsg) {
	t.Helper()
	for _, k := range keys {
		m.Update(k)
	}
}

func surfScreen(m *model) string { return ansi.Strip(m.View()) }

func TestHelpOpensCheatsheetFromKeymap(t *testing.T) {
	m := surfaceModel(t)
	m.command("/help")
	if !m.surfaceOpen() || m.app.modal.kind != "help" {
		t.Fatal("/help on an interactive screen should open the cheatsheet")
	}
	v := surfScreen(m)
	for _, want := range []string{"Keyboard shortcuts", "Anywhere", "Ctrl+K", "clear the conversation"} {
		if !strings.Contains(v, want) {
			t.Fatalf("cheatsheet lacks %q:\n%s", want, v)
		}
	}
	if got := len(strings.Split(m.View(), "\n")); got != 24 {
		t.Fatalf("cheatsheet is %d rows on a 24-row screen", got)
	}
	// Every hinted keymap row is reachable by filtering for its action.
	for _, b := range keymap {
		m.app.modal.filter = b.action
		m.app.modal.refresh(m)
		if len(m.app.modal.rows) == 0 {
			t.Errorf("filtering for %q finds no row", b.action)
		}
	}
	m.app.modal.filter = ""
	pressAll(t, m, keyRunes("d"), keyRunes("o"), keyRunes("c"), keyRunes("k"))
	for _, r := range m.app.modal.rows {
		if !strings.Contains(strings.ToLower(r.section+r.key+r.desc+r.search), "dock") {
			t.Fatalf("filter kept an unrelated row %+v", r)
		}
	}
	pressAll(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if !m.surfaceOpen() || m.app.modal.filter != "" {
		t.Fatal("first Esc clears the filter")
	}
	pressAll(t, m, tea.KeyMsg{Type: tea.KeyTab})
	if !strings.Contains(surfScreen(m), "/clear") {
		t.Fatalf("Tab should show the command tab:\n%s", surfScreen(m))
	}
	pressAll(t, m, keyRunes("/resume"), tea.KeyMsg{Type: tea.KeyEnter})
	if m.surfaceOpen() || m.input.Value() != "/resume " {
		t.Fatalf("Enter on a command inserts it: open=%v input=%q", m.surfaceOpen(), m.input.Value())
	}
}

func TestHelpTextFallback(t *testing.T) {
	m := surfaceModel(t)
	m.command("/help text")
	if m.surfaceOpen() || !strings.Contains(lastBlock(m), "Ctrl+K — clear the conversation") {
		t.Fatal("/help text prints the plain listing")
	}
	c := compactCmdModel()
	c.command("/help")
	if c.surfaceOpen() || !strings.Contains(lastBlock(c), "/resume") {
		t.Fatal("without a screen /help prints the plain listing")
	}
}

func TestSettingsF2StepsAndToggles(t *testing.T) {
	m := surfaceModel(t)
	m.Update(tea.KeyMsg{Type: tea.KeyF2})
	if !m.surfaceOpen() || m.app.modal.kind != "settings" {
		t.Fatal("F2 should open settings")
	}
	for _, want := range []string{"Display", "Agent", "Theme", "Permission mode", "↓ 4 more"} {
		if !strings.Contains(surfScreen(m), want) {
			t.Fatalf("settings lacks %q:\n%s", want, surfScreen(m))
		}
	}
	pressAll(t, m, keyRunes("compaction level"))
	if n := len(m.app.modal.rows); n != 1 {
		t.Fatalf("filter left %d rows", n)
	}
	before := m.compactPct()
	pressAll(t, m, tea.KeyMsg{Type: tea.KeyRight})
	if m.compactPct() != before+10 {
		t.Fatalf("→ should raise the compaction level: %d → %d", before, m.compactPct())
	}
	if !strings.Contains(surfScreen(m), "%") {
		t.Fatal("value should be shown")
	}
	pressAll(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	pressAll(t, m, keyRunes("thinking"))
	was := m.showThinking
	pressAll(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.showThinking == was {
		t.Fatal("Enter should toggle thinking tokens")
	}
	pressAll(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	pressAll(t, m, keyRunes("permission"))
	pressAll(t, m, tea.KeyMsg{Type: tea.KeyRight})
	if m.permissionMode != "plan" {
		t.Fatalf("→ on permission mode: %q", m.permissionMode)
	}
	pressAll(t, m, tea.KeyMsg{Type: tea.KeyEsc}, tea.KeyMsg{Type: tea.KeyEsc})
	if m.surfaceOpen() {
		t.Fatal("Esc closes settings")
	}
	if cfg, err := config.Load(); err != nil || cfg.CompactPct != before+10 {
		t.Fatalf("the change should go through the existing setter and persist: %v %+v", err, cfg.CompactPct)
	}
}

func TestSettingsModelOpensPickerAboveSurface(t *testing.T) {
	m := surfaceModel(t)
	m.command("/settings /model")
	pressAll(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.mpicker == nil {
		t.Fatal("Enter on Model opens the model picker")
	}
	pressAll(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.mpicker != nil || !m.surfaceOpen() {
		t.Fatal("closing the picker returns to settings")
	}
}

func TestThemeSlashArgument(t *testing.T) {
	m := surfaceModel(t)
	prevMono := monoMode
	t.Cleanup(func() { setMonoMode(prevMono, false) })
	for _, name := range []string{"mono", "light", "dark", "auto"} {
		m.command("/theme " + name)
		want := name
		if name == "auto" {
			want = ""
		}
		if m.cfg.Theme != want {
			t.Fatalf("/theme %s left cfg.Theme %q", name, m.cfg.Theme)
		}
		if monoMode != (name == "mono") {
			t.Fatalf("/theme %s: mono=%v", name, monoMode)
		}
	}
	m.command("/theme neon")
	if !strings.Contains(lastBlock(m), "usage: /theme auto|light|dark|mono") {
		t.Fatalf("bad theme: %q", lastBlock(m))
	}
}

func extensionsModel(t *testing.T) *model {
	t.Helper()
	m := surfaceModel(t)
	m.cfg = config.Default()
	off := false
	m.mcpMgr = mcp.NewManager(map[string]mcp.ServerConfig{
		"docs":  {Command: []string{"docs-mcp", "--stdio"}},
		"tasks": {URL: "https://mcp.example.com/tasks", Enabled: &off},
	})
	t.Cleanup(m.mcpMgr.Close)
	m.skillScan = func() []skills.Skill {
		return []skills.Skill{
			{Name: "release", Description: "cut a release and write the changelog", Path: "/skills/release/SKILL.md"},
			{Name: "triage", Description: "label new issues", Path: "/skills/triage/SKILL.md", DisableModelInvocation: true},
		}
	}
	m.cfg.Hooks = map[string][]config.Hook{
		"PreToolUse": {{Command: "scripts/guard.sh", Timeout: 5}},
		"Stop":       {{Command: "notify-send done"}},
	}
	return m
}

func TestExtensionsTabsAndActions(t *testing.T) {
	m := extensionsModel(t)
	m.command("/extensions")
	v := surfScreen(m)
	for _, want := range []string{"Extensions", "[MCP]", "Skills", "Hooks", "Plugins", "docs", "tasks", "docs-mcp --stdio"} {
		if !strings.Contains(v, want) {
			t.Fatalf("extensions lacks %q:\n%s", want, v)
		}
	}
	// Enter disables the selected (enabled) server through the existing setter.
	pressAll(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cfg, ok := m.mcpMgr.Config("docs"); !ok || !cfg.Disabled() {
		t.Fatal("Enter should disable docs")
	}
	if !strings.Contains(surfScreen(m), "docs: disabled (persisted)") {
		t.Fatalf("the action reports in the modal:\n%s", surfScreen(m))
	}
	saved, err := config.Load()
	if err != nil || saved.MCPServers["docs"].Enabled == nil || *saved.MCPServers["docs"].Enabled {
		t.Fatalf("the toggle should persist: %v %+v", err, saved.MCPServers)
	}
	pressAll(t, m, tea.KeyMsg{Type: tea.KeyTab})
	if v := surfScreen(m); !strings.Contains(v, "[Skills]") || !strings.Contains(v, "release") || !strings.Contains(v, "manual") {
		t.Fatalf("skills tab:\n%s", v)
	}
	pressAll(t, m, tea.KeyMsg{Type: tea.KeyTab})
	if v := surfScreen(m); !strings.Contains(v, "PreToolUse") || !strings.Contains(v, "scripts/guard.sh") || !strings.Contains(v, "5s") {
		t.Fatalf("hooks tab:\n%s", v)
	}
	pressAll(t, m, tea.KeyMsg{Type: tea.KeyTab})
	if v := surfScreen(m); !strings.Contains(v, "no plugins installed") {
		t.Fatalf("plugins tab:\n%s", v)
	}
	m.closeSurface()
	m.command("/extensions hooks")
	if m.app.modal.tab != 2 {
		t.Fatal("/extensions hooks opens on the hooks tab")
	}
}

func TestMemoryViewListsAndMarksDone(t *testing.T) {
	m := surfaceModel(t)
	dir, _ := config.Dir()
	if err := os.WriteFile(filepath.Join(dir, "memory.md"), []byte("- [ ] prefers pnpm over npm\n- [x] old fact\n- [ ] the CI runs on Linux and macOS\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.command("/memory")
	if !m.surfaceOpen() || m.app.modal.kind != "memory" {
		t.Fatal("bare /memory opens the memory view")
	}
	v := surfScreen(m)
	for _, want := range []string{"Memory", "installation", "prefers pnpm over npm", "done", "Enter marks it done"} {
		if !strings.Contains(v, want) {
			t.Fatalf("memory view lacks %q:\n%s", want, v)
		}
	}
	pressAll(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	b, _ := os.ReadFile(filepath.Join(dir, "memory.md"))
	if !strings.Contains(string(b), "- [x] prefers pnpm") {
		t.Fatalf("Enter should mark the entry done:\n%s", b)
	}
	m.closeSurface()
	m.command("/memory list")
	if m.surfaceOpen() || !strings.Contains(lastBlock(m), "prefers pnpm") {
		t.Fatal("/memory list keeps the text listing")
	}
}

func TestSurfaceFitsEveryWidth(t *testing.T) {
	for _, open := range []func(m *model){
		func(m *model) { m.command("/help") },
		func(m *model) { m.command("/settings") },
		func(m *model) { m.command("/extensions") },
		func(m *model) { m.command("/memory") },
	} {
		for _, size := range [][2]int{{40, 24}, {80, 24}, {120, 40}, {20, 8}} {
			m := extensionsModel(t)
			m.Update(mkWinSize(size[0], size[1]))
			open(m)
			lines := strings.Split(m.View(), "\n")
			if len(lines) != size[1] {
				t.Fatalf("%s at %v: %d rows", m.app.modal.kind, size, len(lines))
			}
			for _, l := range lines {
				if w := ansi.StringWidth(l); w > size[0] {
					t.Fatalf("%s at %v: row %d cells wide: %q", m.app.modal.kind, size, w, ansi.Strip(l))
				}
			}
		}
	}
}
