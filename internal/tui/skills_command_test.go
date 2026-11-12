package tui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/Stack-Cairn/K-brain/internal/skills"
)

func TestSkillsCommandListsAndInsertsMention(t *testing.T) {
	m := extensionsModel(t)
	m.command("/skills")
	if !m.surfaceOpen() || m.app.modal.kind != "skills" {
		t.Fatal("/skills should open the skills surface")
	}
	v := surfScreen(m)
	for _, want := range []string{"Skills", "release", "triage", "manual", "/skills/release/SKILL.md", "Enter insert"} {
		if !strings.Contains(v, want) {
			t.Fatalf("/skills lacks %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "[MCP]") {
		t.Fatalf("/skills shows only the skills list, not the extension tabs:\n%s", v)
	}
	pressAll(t, m, tea.KeyMsg{Type: tea.KeyDown}, tea.KeyMsg{Type: tea.KeyEnter})
	if m.surfaceOpen() {
		t.Fatal("Enter closes the surface")
	}
	if got := m.input.Value(); got != "$triage " {
		t.Fatalf("Enter inserts the mention, got %q", got)
	}
}

func TestSkillsCommandFilterAndAlias(t *testing.T) {
	m := extensionsModel(t)
	m.command("/skill changelog")
	if !m.surfaceOpen() || m.app.modal.kind != "skills" {
		t.Fatal("/skill is an alias of /skills")
	}
	if m.app.modal.filter != "changelog" || len(m.app.modal.rows) != 1 || m.app.modal.rows[0].key != "release" {
		t.Fatalf("the argument prefills the filter: %q %+v", m.app.modal.filter, m.app.modal.rows)
	}
}

func TestSkillMentionKeepsDraft(t *testing.T) {
	m := extensionsModel(t)
	m.input.SetValue("please")
	m.input.CursorEnd()
	m.command("/extensions skills")
	pressAll(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if got := m.input.Value(); got != "please $release " {
		t.Fatalf("the mention joins the draft at the cursor, got %q", got)
	}
}

func TestSkillsCommandHeadless(t *testing.T) {
	m := extensionsModel(t)
	m.height = 0
	m.command("/skills triage")
	out := ansi.Strip(lastBlock(m))
	if !strings.Contains(out, "Skills (1)") || !strings.Contains(out, "$triage") || strings.Contains(out, "$release") {
		t.Fatalf("headless /skills prints the filtered list:\n%s", out)
	}
}

func TestSkillsCommandAllowedWhileBusy(t *testing.T) {
	for _, c := range []string{"/skills", "/skill review"} {
		if !busyCmd(c) {
			t.Fatalf("%s should run during a turn", c)
		}
	}
}

func TestSkillPreviewShortensHomePath(t *testing.T) {
	m := extensionsModel(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	p := filepath.Join(home, ".liveagent", "skills", "notes", "SKILL.md")
	m.skillScan = func() []skills.Skill {
		return []skills.Skill{{Name: "notes", Description: "take notes", Path: p}}
	}
	m.command("/skills")
	v := surfScreen(m)
	want := filepath.Join("~", ".liveagent", "skills", "notes", "SKILL.md")
	if !strings.Contains(v, want) || strings.Contains(v, home) {
		t.Fatalf("preview shows %q, not the absolute home path:\n%s", want, v)
	}
}
