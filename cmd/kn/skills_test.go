package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/skills"
)

func writeSkill(t *testing.T, root, name, desc string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: " + desc + "\n---\n\n# " + name + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSkillsImportDedup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	wd := t.TempDir()
	t.Chdir(wd)

	writeSkill(t, filepath.Join(home, ".agents", "skills"), "linear", "k-brain's copy")

	writeSkill(t, filepath.Join(home, ".codex", "skills"), "linear", "codex copy")
	writeSkill(t, filepath.Join(home, ".codex", "skills"), "codex-only", "only in codex")

	writeSkill(t, filepath.Join(home, ".claude", "skills"), "linear", "claude copy")
	writeSkill(t, filepath.Join(home, ".claude", "skills"), "codex-only", "claude's codex-only")
	writeSkill(t, filepath.Join(home, ".claude", "skills"), "cloudflare", "only in claude")

	if err := skillsCLI([]string{"import"}); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(home, ".agents", "skills")

	body, err := os.ReadFile(filepath.Join(dest, "linear", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "k-brain's copy") {
		t.Errorf("linear was overwritten: %q", body)
	}

	for _, name := range []string{"codex-only", "cloudflare"} {
		if _, err := os.Stat(filepath.Join(dest, name, "SKILL.md")); err != nil {
			t.Errorf("%s not imported: %v", name, err)
		}
	}

	body, err = os.ReadFile(filepath.Join(dest, "codex-only", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "only in codex") {
		t.Errorf("codex-only should be codex's copy, got %q", body)
	}

	if err := skillsCLI([]string{"import"}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Errorf("dest has %d skills after re-import, want 3", len(entries))
	}
}

func TestSkillsImportDryRun(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Chdir(t.TempDir())
	writeSkill(t, filepath.Join(home, ".claude", "skills"), "cloudflare", "cf")

	if err := skillsCLI([]string{"import", "--dry-run"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".agents", "skills")); !os.IsNotExist(err) {
		t.Errorf("--dry-run created the dest dir: %v", err)
	}
}

func TestSkillsImportNothingToDo(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Chdir(t.TempDir())
	if err := skillsCLI([]string{"import"}); err != nil {
		t.Fatal(err)
	}
}

func TestSkillsImportContinuesPastFailure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Chdir(t.TempDir())

	writeSkill(t, filepath.Join(home, ".claude", "skills"), "good", "fine")
	badDir := filepath.Join(home, ".claude", "skills", "bad")
	if err := os.MkdirAll(badDir, 0o755); err != nil {
		t.Fatal(err)
	}
	badFile := filepath.Join(badDir, "SKILL.md")
	if err := os.WriteFile(badFile, []byte("---\nname: bad\ndescription: x\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	sub := filepath.Join(badDir, "refs")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing.md"), filepath.Join(sub, "missing.md")); err != nil {
		t.Skipf("cannot create broken symlink fixture: %v", err)
	}

	err := skillsCLI([]string{"import"})
	if err == nil {
		t.Fatal("import should report the failed skill")
	}
	if !strings.Contains(err.Error(), "bad") {
		t.Errorf("error should name the failed skill, got %v", err)
	}

	if _, err := os.Stat(filepath.Join(home, ".agents", "skills", "good", "SKILL.md")); err != nil {
		t.Errorf("good skill not imported despite the failure: %v", err)
	}

	if _, err := os.Stat(filepath.Join(home, ".agents", "skills", "bad")); err == nil {

		entries, _ := os.ReadDir(filepath.Join(home, ".agents", "skills", "bad"))
		for _, e := range entries {
			if e.Name() == "SKILL.md" {
				t.Error("partial import: bad skill's SKILL.md copied despite the failure")
			}
		}
	}
}

func TestSkillsListCLI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	wd := t.TempDir()
	t.Chdir(wd)
	writeSkill(t, filepath.Join(home, ".agents", "skills"), "linear", "k-brain's copy")
	writeSkill(t, filepath.Join(home, ".claude", "skills"), "cloudflare", "claude copy")

	if err := skillsCLI([]string{"list"}); err != nil {
		t.Fatal(err)
	}
}

func TestSkillsCLIUnknownSubcommand(t *testing.T) {
	if err := skillsCLI([]string{"nope"}); err == nil {
		t.Error("expected error for unknown subcommand")
	}
}

func TestSkillsForeignDirs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	dirs := skills.ForeignDirs()
	if len(dirs) != 2 {
		t.Fatalf("got %d foreign dirs, want 2", len(dirs))
	}
	if !strings.HasSuffix(dirs[0], filepath.Join(".codex", "skills")) {
		t.Errorf("first foreign dir = %q, want codex", dirs[0])
	}
	if !strings.HasSuffix(dirs[1], filepath.Join(".claude", "skills")) {
		t.Errorf("second foreign dir = %q, want claude", dirs[1])
	}
}

func TestCopyDirErrorPaths(t *testing.T) {
	src := filepath.Join(t.TempDir(), "not-a-dir")
	if err := copyDir(src, filepath.Join(t.TempDir(), "dst")); err == nil {
		t.Error("copyDir of a non-directory should fail")
	}
	dst := filepath.Join(t.TempDir(), "exists")
	if err := os.MkdirAll(dst, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := copyDir(t.TempDir(), dst); err == nil {
		t.Error("copyDir to an existing destination should fail")
	}
}

func TestCopyFileErrorPaths(t *testing.T) {
	if err := copyFile(filepath.Join(t.TempDir(), "missing"), filepath.Join(t.TempDir(), "dst")); err == nil {
		t.Error("copyFile of a missing source should fail")
	}
}

func TestSkillsImportKeepsPreExistingDirOnConflict(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Chdir(t.TempDir())

	userDir := filepath.Join(home, ".agents", "skills", "notes")
	if err := os.MkdirAll(userDir, 0o750); err != nil {
		t.Fatal(err)
	}
	userFile := filepath.Join(userDir, "keep-me.txt")
	if err := os.WriteFile(userFile, []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}

	writeSkill(t, filepath.Join(home, ".claude", "skills"), "notes", "claude copy")

	err := skillsCLI([]string{"import"})
	if err == nil {
		t.Fatal("import should report the destination conflict")
	}

	if _, err := os.Stat(userFile); err != nil {
		t.Errorf("pre-existing user folder was deleted or modified: %v", err)
	}
	data, _ := os.ReadFile(userFile)
	if string(data) != "precious" {
		t.Errorf("user file contents changed: %q", data)
	}
}

func TestSkillsImportPathTraversal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Chdir(t.TempDir())

	malDir := filepath.Join(home, ".claude", "skills", "pwn")
	if err := os.MkdirAll(malDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(malDir, "SKILL.md"), []byte("---\nname: ../../pwned\ndescription: x\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	writeSkill(t, filepath.Join(home, ".claude", "skills"), "good", "fine")

	err := skillsCLI([]string{"import"})
	if err == nil {
		t.Fatal("import should reject the traversal name")
	}
	if !strings.Contains(err.Error(), "../../pwned") {
		t.Errorf("error should name the rejected skill, got %v", err)
	}

	if _, err := os.Stat(filepath.Join(home, "pwned")); !os.IsNotExist(err) {
		t.Errorf("path traversal succeeded: %v", err)
	}

	if _, err := os.Stat(filepath.Join(home, ".agents", "skills", "good", "SKILL.md")); err != nil {
		t.Errorf("good skill not imported despite the traversal rejection: %v", err)
	}
}
