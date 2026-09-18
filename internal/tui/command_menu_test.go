package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/Stack-Cairn/K-brain/internal/prompts/templates"
)

func TestSlashMenuDescriptionsExcludeArguments(t *testing.T) {
	m := compactCmdModel()
	for _, language := range []string{"en", "zh_cn"} {
		m.cfg.Language = language
		_, candidates := m.promptCompletions("/")
		for _, candidate := range candidates {
			entry := registryFind(candidate.Text)
			if entry == nil {
				continue
			}
			if candidate.Desc != m.tr(entry.Hint) || strings.HasPrefix(candidate.Desc, "—") {
				t.Fatalf("unexpected description for %s: %q", entry.Name, candidate.Desc)
			}
			if entry.Args != "" && strings.Contains(candidate.Desc, entry.Args) {
				t.Fatalf("arguments leaked into %s menu: %q", entry.Name, candidate.Desc)
			}
			if !strings.Contains(helpTextFor(language), entry.Name+" "+entry.helpHint(language)) {
				t.Fatalf("help lost full usage for %s", entry.Name)
			}
		}
	}
}

func TestCommandArgumentHint(t *testing.T) {
	m := compactCmdModel()
	m.promptCatalog.items = []templates.Template{{Name: "review", ArgumentHint: "<branch>", Description: "Review changes"}}
	for _, tc := range []struct{ input, want string }{
		{"/", ""}, {"/di", ""}, {"/diff", "[--staged|--stat]"},
		{"/diff ", "[--staged|--stat]"}, {"/diff --stat", ""},
		{"/diff --stat ", ""}, {"/diff\n", ""}, {"hello /diff", ""},
		{"/help", ""}, {"/unknown", ""}, {"/review ", "<branch>"},
	} {
		m.input.SetValue(tc.input)
		if got := m.commandArgumentHint(); got != tc.want {
			t.Errorf("%q: got %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestCommandArgumentGhostPreservesInputAndLayout(t *testing.T) {
	m := compactCmdModel()
	m.applyAppearance()
	for _, input := range []string{"/diff", "/diff "} {
		m.input.SetValue(input)
		m.input.CursorEnd()
		before := m.input.View()
		after := m.inputArgumentView(before)
		if !strings.Contains(ansi.Strip(after), "/diff  [--staged|--stat]") && !strings.Contains(ansi.Strip(after), "/diff [--staged|--stat]") {
			t.Fatalf("missing inline ghost: %q", ansi.Strip(after))
		}
		if m.input.Value() != input || lipgloss.Width(before) != lipgloss.Width(after) || lipgloss.Height(before) != lipgloss.Height(after) {
			t.Fatal("ghost changed the draft or input dimensions")
		}
		m.input.CursorStart()
		before = m.input.View()
		if m.inputArgumentView(before) != before {
			t.Fatal("hint should be hidden while editing the command name")
		}
	}
}

func TestSlashTabShowsGhostWithoutInsertingArguments(t *testing.T) {
	m := compactCmdModel()
	m.applyAppearance()
	m = typeStr(t, m, "/di")
	m = pressKey(m, tea.KeyTab)
	if m.input.Value() != "/diff" {
		t.Fatal(m.input.Value())
	}
	view := ansi.Strip(m.inputArgumentView(m.input.View()))
	if !strings.Contains(view, "[--staged|--stat]") {
		t.Fatal(view)
	}
	if strings.Contains(m.menuView(), "[--staged|--stat]") {
		t.Fatal("menu leaked usage")
	}
	m = pressKey(m, tea.KeyEnter)
	if m.input.Value() != "/diff " {
		t.Fatal(m.input.Value())
	}
	if m.menu == nil || len(m.menu.cands) != 2 {
		t.Fatal("missing argument completions")
	}
}

func TestDiffArgumentCompletion(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  []string
	}{
		{"/diff ", []string{"--staged", "--stat"}},
		{"/diff --sta", []string{"--staged", "--stat"}},
		{"/diff --staged ", []string{"--stat"}},
		{"/diff --stat ", []string{"--staged"}},
		{"/diff --stat --staged ", nil},
	} {
		_, got := completions(tc.input, nil, nil, nil, nil)
		if strings.Join(texts(got), ",") != strings.Join(tc.want, ",") {
			t.Errorf("%q: %v", tc.input, texts(got))
		}
	}
}

func TestCommandMenuStableColumnsAndBounds(t *testing.T) {
	m := compactCmdModel()
	candidates := []cand{{"/a", "description"}, {"/b", "description"}, {"/c", "description"}, {"/d", "description"}, {"/e", "description"}, {"/f", "description"}, {"/g", "description"}, {"/h", "description"}, {"/long-command", "description"}}
	m.menu = &menu{cands: candidates}
	first := strings.Split(ansi.Strip(m.menuView()), "\n")[1]
	m.menu.idx = 8
	scrolled := strings.Split(ansi.Strip(m.menuView()), "\n")[0]
	if strings.Index(first, "description") != strings.Index(scrolled, "description") {
		t.Fatal("description column shifted on scroll")
	}
	for _, width := range []int{1, 2, 5, 12, 25, 45, 80} {
		m.width = width
		for _, line := range strings.Split(m.menuView(), "\n") {
			if !utf8.ValidString(line) || lipgloss.Width(line) > width {
				t.Fatalf("width %d: %q", width, line)
			}
		}
		m.input.SetWidth(width)
		m.input.SetValue("/diff ")
		m.input.CursorEnd()
		before := m.input.View()
		after := m.inputArgumentView(before)
		if lipgloss.Width(after) > lipgloss.Width(before) || lipgloss.Height(after) != lipgloss.Height(before) {
			t.Fatalf("ghost overflow at %d", width)
		}
	}
}

func TestPromptTemplateMenuHidesArguments(t *testing.T) {
	root := t.TempDir()
	t.Setenv("K_BRAIN_HOME", root)
	dir := filepath.Join(root, "prompts")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: review\ndescription: Review changes\nargument-hint: <branch> [--fix]\n---\nReview $1"
	if err := os.WriteFile(filepath.Join(dir, "review.md"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	m := compactCmdModel()
	_, candidates := m.promptCompletions("/review")
	if len(candidates) != 1 || candidates[0].Desc != "Review changes" {
		t.Fatalf("%+v", candidates)
	}
	m.input.SetValue("/review ")
	if m.commandArgumentHint() != "<branch> [--fix]" {
		t.Fatalf("hint: %q", m.commandArgumentHint())
	}
}

func TestPaletteCommandColumnsHideUsage(t *testing.T) {
	m := compactCmdModel()
	m.width = 100
	m.openPalette()
	view := ansi.Strip(m.paletteView())
	for _, usage := range []string{"[level]", "[zh_cn|en]", "[title]", "<model>"} {
		if strings.Contains(view, usage) {
			t.Fatalf("palette leaked %q", usage)
		}
	}
	for _, language := range []string{"en", "zh_cn"} {
		m.cfg.Language = language
		for _, width := range []int{35, 60, 100} {
			m.width = width
			for _, line := range strings.Split(m.paletteView(), "\n") {
				plain := ansi.Strip(line)
				if strings.Contains(plain, "/") && !strings.Contains(plain, "↑/↓") && lipgloss.Width(line) > width {
					t.Fatalf("palette row exceeds %d: %q", width, plain)
				}
			}
		}
	}
}
