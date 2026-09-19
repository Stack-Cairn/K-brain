package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/i18n"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func TestLanguageCommandPersistsAndSwitches(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	m := compactCmdModel()
	m.input.SetValue("keep 草稿")
	messages := len(m.agent.Messages)
	m.command("/language zh_cn")
	if m.language() != "zh_cn" || m.input.Value() != "keep 草稿" || len(m.agent.Messages) != messages {
		t.Fatal("language switch changed conversation or draft")
	}
	cfg, err := config.Load()
	if err != nil || cfg.Language != "zh_cn" {
		t.Fatalf("persist: %v %+v", err, cfg)
	}
	_, cs := m.promptCompletions("/model")
	if len(cs) == 0 || !strings.Contains(cs[0].Desc, "选择模型") {
		t.Fatal(cs)
	}
	m.command("/help")
	if !strings.Contains(lastBlock(m), "选择界面语言") || !strings.Contains(lastBlock(m), "换行") {
		t.Fatal("help not localized")
	}
	m.input.Reset()
	m.syncInputPlaceholder()
	if !strings.Contains(m.input.Placeholder, "氪脑") {
		t.Fatal(m.input.Placeholder)
	}
	m.busy = true
	m.command("/language en")
	if m.language() != "en" || !busyCmd("/language en") {
		t.Fatal("busy switch failed")
	}
	if !strings.Contains(m.input.Placeholder, "busy") {
		t.Fatal(m.input.Placeholder)
	}
	_, cs = m.promptCompletions("/model")
	if !strings.Contains(cs[0].Desc, "switch model") {
		t.Fatal(cs)
	}
	if registryFind("/model").Hint == cs[0].Desc && strings.Contains(commands[0].Desc, "选择") {
		t.Fatal("global registry changed")
	}
}

func TestLanguagePickerAndCancel(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	m := compactCmdModel()
	m.command("/language")
	if m.palette == nil || m.palette.top().kind != panelLanguage {
		t.Fatal("missing picker")
	}
	pp := m.palette.top()
	if pp.midx != 2 || len(pp.list) != 3 {
		t.Fatalf("%+v", pp)
	}
	m.paletteKey(tea.KeyMsg{Type: tea.KeyUp})
	m.paletteKey(tea.KeyMsg{Type: tea.KeyEsc})
	if m.language() != "en" || m.palette != nil {
		t.Fatal("cancel committed language")
	}
	m.command("/language")
	m.paletteKey(tea.KeyMsg{Type: tea.KeyUp})
	m.paletteKey(tea.KeyMsg{Type: tea.KeyUp})
	m.paletteKey(tea.KeyMsg{Type: tea.KeyEnter})
	if m.language() != "zh_cn" || m.palette != nil {
		t.Fatal("selection did not apply")
	}
	m.command("/language")
	view := ansi.Strip(m.paletteView())
	if !strings.Contains(view, "简体中文") || !strings.Contains(view, "繁體中文") || !strings.Contains(view, "English") || !strings.Contains(view, "当前") {
		t.Fatal(view)
	}
}

func TestLanguageInvalidAndSaveFailure(t *testing.T) {
	root := t.TempDir()
	t.Setenv("K_BRAIN_HOME", root)
	m := compactCmdModel()
	for _, command := range []string{"/language fr", "/language zh_cn extra"} {
		m.command(command)
		if m.language() != "en" || !strings.Contains(lastBlock(m), "usage:") {
			t.Fatal(command)
		}
	}
	if err := os.Mkdir(filepath.Join(root, "config.json.tmp"), 0700); err != nil {
		t.Fatal(err)
	}
	m.command("/language zh_cn")
	if m.language() != "en" || !strings.Contains(lastBlock(m), "could not save") {
		t.Fatal("save failure must roll back")
	}
}

func TestLanguageRegistryCoverageAndWidth(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	for _, e := range registry {
		if i18n.Text("zh_cn", e.Hint) == e.Hint {
			t.Errorf("missing translation for %s", e.Name)
		}
	}
	m := compactCmdModel()
	m.cfg.Language = "zh_cn"
	_, cs := m.promptCompletions("/")
	for _, width := range []int{20, 45, 80, 120} {
		m.width = width
		m.menu = &menu{cands: cs}
		for _, line := range strings.Split(m.menuView(), "\n") {
			if !utf8.ValidString(line) || lipgloss.Width(line) > width {
				t.Fatalf("width %d: %q", width, line)
			}
		}
	}
	_, options := m.promptCompletions("/language ")
	if len(options) != 3 {
		t.Fatal(options)
	}
	_, filtered := m.promptCompletions("/language zh")
	if len(filtered) != 2 || filtered[0].Text != "zh_cn" || filtered[1].Text != "zh_tw" {
		t.Fatal(filtered)
	}
	other := compactCmdModel()
	_, en := other.promptCompletions("/model")
	if !strings.Contains(en[0].Desc, "switch model") {
		t.Fatal("language leaked between instances")
	}
}

func TestLanguageConfigSyncAndPaletteSearch(t *testing.T) {
	m := compactCmdModel()
	language := "zh_cn"
	m.cfgExtra = map[string]string{"theme": "dark"}
	m.applyCfgSync(cfgSyncMsg{language: &language})
	if m.language() != language {
		t.Fatal("theme pin blocked language sync")
	}
	m.openPalette()
	m.palette.filter = "语言"
	m.palette.applyFilter(m)
	if len(m.palette.items) != 1 || m.palette.items[0].title != "Language" {
		t.Fatal("Chinese search failed")
	}
}
