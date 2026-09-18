package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestProvisionSubagentWorktree(t *testing.T) {
	if os.Getenv("K_BRAIN_SKIP_WORKTREE_TEST") == "1" {

		t.Skip("skipped via K_BRAIN_SKIP_WORKTREE_TEST")
	}
	ctx := context.Background()
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init")
	git("config", "user.email", "t@t")
	git("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "init")

	t.Chdir(repo)

	path, err := provisionSubagentWorktree(ctx, "task-42")
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if _, err := os.Stat(filepath.Join(path, "f.txt")); err != nil {
		t.Fatalf("worktree should contain the committed file: %v", err)
	}

	out, _ := exec.CommandContext(ctx, "git", "-C", repo, "branch", "--list", "subagent/task-42").Output()
	if len(out) == 0 {
		t.Fatalf("expected branch subagent/task-42, branches: %s", out)
	}

	_ = exec.CommandContext(ctx, "git", "-C", repo, "worktree", "remove", "--force", path).Run()
}

func TestProvisionSubagentWorktreeNotARepo(t *testing.T) {
	t.Chdir(t.TempDir())
	if _, err := provisionSubagentWorktree(context.Background(), "task-9"); err == nil {
		t.Fatal("expected an error outside a git repo")
	}
}

func TestWorktreeOverrideBeatsDefault(t *testing.T) {

	resolve := func(def bool, ov *bool) bool {
		if ov != nil {
			return *ov
		}
		return def
	}
	on, off := true, false
	if !resolve(false, &on) {
		t.Fatal("worktree=true should override session default false")
	}
	if resolve(true, &off) {
		t.Fatal("worktree=false should override session default true")
	}
	if !resolve(true, nil) {
		t.Fatal("no override should fall back to session default")
	}
}
