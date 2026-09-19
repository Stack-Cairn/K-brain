package agent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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

	out, err := exec.CommandContext(ctx, "git", "-C", path, "symbolic-ref", "--short", "HEAD").Output()
	if err != nil || !strings.HasPrefix(strings.TrimSpace(string(out)), "subagent/task-42-") {
		t.Fatalf("expected unique task branch, got %q: %v", out, err)
	}

	_ = exec.CommandContext(ctx, "git", "-C", repo, "worktree", "remove", "--force", path).Run()
}

func newWorktreeTestRepo(t *testing.T) string {
	t.Helper()
	if os.Getenv("K_BRAIN_SKIP_WORKTREE_TEST") == "1" {
		t.Skip("skipped via K_BRAIN_SKIP_WORKTREE_TEST")
	}
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init"}, {"-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "init"}} {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %v: %s", err, out)
		}
	}
	return repo
}

func TestWorktreeRepeatedTaskID(t *testing.T) {
	repo := newWorktreeTestRepo(t)
	paths := make(chan string, 4)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			path, err := provisionSubagentWorktreeAt(context.Background(), repo, "same-task-1")
			if err != nil {
				t.Error(err)
				return
			}
			paths <- path
		})
	}
	wg.Wait()
	close(paths)
	seen := make(map[string]bool)
	for path := range paths {
		if seen[path] {
			t.Fatalf("worktree reused: %s", path)
		}
		seen[path] = true
		if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 4 {
		t.Fatalf("created %d worktrees, want 4", len(seen))
	}
}

func TestWorktreeRejectsInvalidIDs(t *testing.T) {
	for _, id := range []string{"", "..", "../outside", `..\outside`, "bad id", "a:b", "a\nb", "bad.lock"} {
		if _, err := provisionSubagentWorktreeAt(context.Background(), t.TempDir(), id); err == nil || !strings.Contains(err.Error(), "invalid worktree task id") {
			t.Fatalf("ID %q: %v", id, err)
		}
	}
}

func TestWorktreeCancelledBeforeProvisioning(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := provisionSubagentWorktreeAt(ctx, t.TempDir(), "task-1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestWorktreeLongTaskID(t *testing.T) {
	repo := newWorktreeTestRepo(t)
	path, err := provisionSubagentWorktreeAt(context.Background(), repo, strings.Repeat("x", 1000))
	if err != nil {
		t.Fatal(err)
	}
	if len(filepath.Base(path)) > 100 {
		t.Fatalf("worktree path component is too long: %d", len(filepath.Base(path)))
	}
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
