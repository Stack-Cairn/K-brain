package skills

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestExternalNestedSkillImport(t *testing.T) {
	home := externalTestHome(t)
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	m, err := NewManager()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(home, ".gemini", "skills")
	src := fixtureSkill(t, filepath.Join(root, "collection", "skills"), "nested")
	reference := filepath.Join(src, "reference.md")
	if err := os.WriteFile(reference, []byte("nested reference"), 0600); err != nil {
		t.Fatal(err)
	}
	scan := externalTool(t, m.External(), "gemini")
	if len(scan.Skills) != 1 || scan.Skills[0].Pack != "collection" || scan.Skills[0].BaseDir != src {
		t.Fatalf("unexpected pack scan: %+v", scan)
	}
	installed, err := m.Install(context.Background(), scan.Skills[0].BaseDir, "", "fail", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if installed.Name != "nested" {
		t.Fatalf("unexpected import: %+v", installed)
	}
	content, _, _, err := m.Read("nested/reference.md", 0, 200)
	if err != nil || content != "nested reference" {
		t.Fatalf("imported reference: %q, %v", content, err)
	}
	original, err := os.ReadFile(reference)
	if err != nil || string(original) != "nested reference" {
		t.Fatalf("source reference changed: %q, %v", original, err)
	}
}

func TestExternalImportRejectsEscapingLinks(t *testing.T) {
	home := externalTestHome(t)
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	m, err := NewManager()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(home, ".gemini", "skills")
	pack := filepath.Join(root, "collection")
	src := fixtureSkill(t, pack, "nested")
	outside := fixtureSkill(t, t.TempDir(), "outside")
	if err := os.Symlink(outside, filepath.Join(pack, "escape")); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	scan := externalTool(t, m.External(), "gemini")
	if len(scan.Skills) != 1 || scan.Skills[0].BaseDir != src {
		t.Fatalf("scan followed an escaping link: %+v", scan)
	}
	if err := os.Symlink(filepath.Join(outside, "SKILL.md"), filepath.Join(src, "reference.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Install(context.Background(), scan.Skills[0].BaseDir, "", "fail", nil, nil); err == nil {
		t.Fatal("import accepted a skill containing a symbolic link")
	}
	if _, err := os.Stat(filepath.Join(m.Root(), "nested")); !os.IsNotExist(err) {
		t.Fatalf("rejected skill was installed: %v", err)
	}
}
