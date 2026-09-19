package skills

import (
	"path/filepath"
	"testing"
)

func TestProjectSkillIndexed(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Chdir(project)
	root := filepath.Join(project, ".agents", "skills")
	writeSkill(t, root, "review", "---\nname: review\ndescription: Review local code changes\n---\n")
	idx, problems := ScanDetailed(DefaultDirs()...)
	if len(problems) != 0 || len(idx) != 1 {
		t.Fatalf("project skill index: %+v, problems: %+v", idx, problems)
	}
	if idx[0].Name != "review" || idx[0].Description != "Review local code changes" || idx[0].Path != filepath.Join(root, "review", "SKILL.md") {
		t.Fatalf("project skill: %+v", idx[0])
	}
}
