package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/mcp"
	"github.com/Stack-Cairn/K-brain/internal/skills"
)

func TestDoctorFreshSession(t *testing.T) {
	m := tasksModel("http://unused")
	m.sysPrompt = "You are an expert coding assistant operating inside k-brain. "
	disabled := false
	m.mcpMgr = mcp.NewManager(map[string]mcp.ServerConfig{
		"off":     {Command: []string{"true"}, Enabled: &disabled},
		"invalid": {},
	})
	out := m.doctorReport()
	for _, want := range []string{
		"context audit",
		"system prompt (base)",
		"skills (",
		"tool schemas (",
		"mcp: off",
		"disabled",
		"mcp: invalid",
		"TOTAL injected",
		"Trim:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestDoctorCommandWired(t *testing.T) {
	m := tasksModel("http://unused")
	m.command("/context-doctor")
	if len(m.blocks) == 0 || !strings.Contains(m.blocks[len(m.blocks)-1].text, "context audit") {
		t.Fatalf("/context-doctor produced no report: %+v", m.blocks)
	}

	before := len(m.blocks)
	m.command("/doctor")
	if !strings.Contains(m.blocks[len(m.blocks)-1].text, "unknown command") {
		t.Error("/doctor should not exist as a shorthand")
	}
	_ = before
}

func TestDoctorSkillSources(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	proj := t.TempDir()
	t.Chdir(proj)

	writeSkill := func(dir, name, desc string) {
		t.Helper()
		sd := filepath.Join(dir, name)
		if err := os.MkdirAll(sd, 0o755); err != nil {
			t.Fatal(err)
		}
		body := "---\nname: " + name + "\ndescription: " + desc + "\n---\n"
		if err := os.WriteFile(filepath.Join(sd, "SKILL.md"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeSkill(filepath.Join(proj, ".agents", "skills"), "proj-skill", "from the project")
	writeSkill(filepath.Join(home, ".k-brain", "skills"), "user-skill", "from the user dir")

	m := tasksModel("http://unused")
	m.skillScan = func() []skills.Skill { return skills.Scan(skills.DefaultDirs()...) }
	out := m.doctorReport()
	if !strings.Contains(out, "proj-skill") || !strings.Contains(out, "user-skill") {
		t.Fatalf("both skills should be named:\n%s", out)
	}
	if !strings.Contains(out, "proj-skill ~") || !strings.Contains(out, "(./.agents/skills)") {
		t.Errorf("project skill should point at ./.agents/skills:\n%s", out)
	}
	if !strings.Contains(out, "(~/.k-brain/skills)") {
		t.Errorf("user skill should point at ~/.k-brain/skills:\n%s", out)
	}
}

func TestShortSkillsDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	wd := t.TempDir()
	t.Chdir(wd)
	cases := map[string]string{
		filepath.Join(home, ".k-brain", "skills"):                  "~/.k-brain/skills",
		filepath.Join(wd, ".agents", "skills"):                     "./.agents/skills",
		filepath.Join(string(filepath.Separator), "opt", "skills"): "/opt/skills",
	}
	for dir, want := range cases {
		if got := shortSkillsDir(dir); got != want {
			t.Errorf("shortSkillsDir(%q) = %q, want %q", dir, got, want)
		}
	}
}

func TestTok(t *testing.T) {
	if tok(350) != "350" || tok(4848) != "4.8k" {
		t.Errorf("tok: %q %q", tok(350), tok(4848))
	}
}
