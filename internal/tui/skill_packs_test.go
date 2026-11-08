package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/skills"
)

func TestSkillPackSkillsComplete(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("LIVEAGENT_HOME", filepath.Join(home, ".liveagent"))
	t.Chdir(t.TempDir())
	for rel, body := range map[string]string{
		"demo-pack/skills/SKILL.md":         "---\nname: demo-router\ndescription: Routes tasks\n---\n",
		"demo-pack/skills/radare2/SKILL.md": "---\nname: radare2\ndescription: |\n  Use radare2 for binaries.\n---\n",
		"plain/SKILL.md":                    "---\nname: plain\ndescription: A top-level skill\n---\n",
	} {
		p := filepath.Join(home, ".liveagent", "skills", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m := surfaceModel(t)
	_, cands := completions("look at this with $rad", nil, nil, m.skillCands(), nil)
	if len(cands) != 1 || cands[0].Text != "$radare2" || cands[0].Desc != "[demo-pack] Use radare2 for binaries." {
		t.Fatalf("completions = %+v", cands)
	}
	_, cands = completions("$", nil, nil, m.skillCands(), nil)
	var got []string
	for _, c := range cands {
		got = append(got, c.Text)
	}
	for _, want := range []string{"$demo-router", "$radare2", "$plain"} {
		if !strings.Contains(strings.Join(got, " "), want) {
			t.Fatalf("completions %v lack %s", got, want)
		}
	}
}

func TestSkillSurfaceRowsGroupPacks(t *testing.T) {
	m := surfaceModel(t)
	m.skillScan = func() []skills.Skill {
		return []skills.Skill{
			{Name: "radare2", Description: "r2", Path: "/s/demo-pack/skills/radare2/SKILL.md", Pack: "demo-pack", BaseDir: "demo-pack/skills/radare2"},
			{Name: "release", Description: "cut a release", Path: "/s/release/SKILL.md", BaseDir: "release"},
			{Name: "zz", Description: "z", Path: "/s/a-pack/zz/SKILL.md", Pack: "a-pack", BaseDir: "a-pack/zz"},
			{Name: "triage", Description: "label issues", Path: "/s/triage/SKILL.md", BaseDir: "triage"},
		}
	}
	rows := skillSurfaceRows(m)
	var got []string
	for _, r := range rows {
		got = append(got, r.section+"/"+r.key)
	}
	want := "/release,/triage,pack: a-pack/zz,pack: demo-pack/radare2"
	if strings.Join(got, ",") != want {
		t.Fatalf("rows = %v, want %s", got, want)
	}
	preview := strings.Join(rows[3].preview(m), "\n")
	if !strings.Contains(preview, "skill pack demo-pack · demo-pack/skills/radare2") {
		t.Fatalf("preview:\n%s", preview)
	}
}
