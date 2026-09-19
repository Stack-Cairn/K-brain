package tui

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func rewindRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	git(t, repo, "init", "-q")
	git(t, repo, "config", "user.email", "t@t")
	git(t, repo, "config", "user.name", "t")
	git(t, repo, "config", "core.autocrlf", "false")
	writeRewindFile(t, repo, "a.txt", "base\n")
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-qm", "base")
	t.Chdir(repo)
	return repo
}

func writeRewindFile(t *testing.T, repo, name, body string) {
	t.Helper()
	path := filepath.Join(repo, name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func rewindGit(t *testing.T, args ...string) string {
	t.Helper()
	out, err := gitOut(context.Background(), args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestWorkspaceRestoreIndexAndWholeRepository(t *testing.T) {
	repo := rewindRepo(t)
	writeRewindFile(t, repo, "a.txt", "staged\n")
	git(t, repo, "add", "a.txt")
	writeRewindFile(t, repo, "a.txt", "unstaged\n")
	writeRewindFile(t, repo, "nested/b.txt", "before\n")
	git(t, repo, "add", "nested/b.txt")
	before := rewindGit(t, "status", "--porcelain")
	snap := snapshotWorkspace()
	if snap == "" {
		t.Fatal("snapshot failed")
	}
	writeRewindFile(t, repo, "a.txt", "agent edit\n")
	writeRewindFile(t, repo, "nested/b.txt", "agent edit\n")
	writeRewindFile(t, repo, "added.txt", "agent added\n")
	git(t, repo, "add", "-A")
	writeRewindFile(t, repo, "mine.txt", "keep me\n")
	t.Chdir(filepath.Join(repo, "nested"))
	if _, err := restoreWorkspace(snap); err != nil {
		t.Fatal(err)
	}
	if got := rewindGit(t, "show", ":a.txt"); got != "staged" {
		t.Errorf("index = %q, want staged", got)
	}
	for name, want := range map[string]string{"a.txt": "unstaged\n", "nested/b.txt": "before\n", "mine.txt": "keep me\n"} {
		got, err := os.ReadFile(filepath.Join(repo, name))
		if err != nil || string(got) != want {
			t.Errorf("%s = %q, %v; want %q", name, got, err, want)
		}
	}
	if _, err := os.Stat(filepath.Join(repo, "added.txt")); !os.IsNotExist(err) {
		t.Errorf("post-snapshot tracked addition was not removed: %v", err)
	}
	if got := rewindGit(t, "status", "--porcelain", "--untracked-files=no"); got != before {
		t.Errorf("status = %q, want %q", got, before)
	}
}

func TestWorkspaceRestoreProtectsUntrackedCollision(t *testing.T) {
	for _, ignored := range []bool{false, true} {
		t.Run(map[bool]string{false: "untracked", true: "ignored"}[ignored], func(t *testing.T) {
			repo := rewindRepo(t)
			snap := snapshotWorkspace()
			if snap == "" {
				t.Fatal("snapshot failed")
			}
			git(t, repo, "rm", "--cached", "a.txt")
			writeRewindFile(t, repo, "a.txt", "new user data\n")
			if ignored {
				writeRewindFile(t, repo, ".gitignore", "a.txt\n")
			}
			before := rewindGit(t, "status", "--porcelain")
			if _, err := restoreWorkspace(snap); err == nil {
				t.Error("expected untracked collision error")
			}
			got, err := os.ReadFile(filepath.Join(repo, "a.txt"))
			if err != nil || string(got) != "new user data\n" {
				t.Errorf("untracked file overwritten: %q, %v", got, err)
			}
			if got := rewindGit(t, "status", "--porcelain"); got != before {
				t.Errorf("index changed: %q != %q", got, before)
			}
			rewindGit(t, "rev-parse", "--verify", "refs/k-brain/snapshots/"+snap)
		})
	}
}

func TestRewindFailurePreservesConversationAndDraft(t *testing.T) {
	rewindRepo(t)
	m := rewindModel(t,
		ai.Message{Role: "user", Content: "q1", Authored: true},
		ai.Message{Role: "assistant", Content: "a1"},
	)
	ref := strings.Repeat("1", 40)
	m.snapshots = map[int]string{1: ref}
	if err := m.store.SetSnapshot(m.sessionID, 1, ref); err != nil {
		t.Fatal(err)
	}
	before := append([]ai.Message(nil), m.agent.Messages...)
	_, persistedBefore, err := m.store.Load(m.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	m.input.SetValue("my draft")
	m.openRewind()
	m.rewindKey(tea.KeyMsg{Type: tea.KeyEnter})
	if !reflect.DeepEqual(m.agent.Messages, before) || len(m.future) != 0 {
		t.Fatal("failed workspace restore changed conversation")
	}
	if m.snapshots[1] != ref || m.store.Snapshots(m.sessionID)[1] != ref {
		t.Fatal("failed workspace restore discarded snapshot")
	}
	_, persistedAfter, err := m.store.Load(m.sessionID)
	if err != nil || !reflect.DeepEqual(persistedBefore, persistedAfter) {
		t.Fatalf("persisted conversation changed: %v", err)
	}
	if m.input.Value() != "my draft" {
		t.Fatal("failed rewind replaced input draft")
	}
}

func TestWorkspaceRestoreCleanSnapshotAndLineEndings(t *testing.T) {
	for _, autocrlf := range []string{"false", "true"} {
		t.Run(autocrlf, func(t *testing.T) {
			repo := rewindRepo(t)
			git(t, repo, "config", "core.autocrlf", autocrlf)
			snap := snapshotWorkspace()
			if snap == "" {
				t.Fatal("snapshot failed")
			}
			writeRewindFile(t, repo, "a.txt", "new staged content\n")
			git(t, repo, "add", "a.txt")
			n, err := restoreWorkspace(snap)
			if err != nil || n != 1 {
				t.Fatalf("restore = %d, %v; want 1, nil", n, err)
			}
			want := "base\n"
			if autocrlf == "true" {
				want = "base\r\n"
			}
			got, err := os.ReadFile(filepath.Join(repo, "a.txt"))
			if err != nil || string(got) != want {
				t.Fatalf("restored content = %q, %v; want %q", got, err, want)
			}
			if !workspaceClean() {
				t.Fatal("clean snapshot did not restore clean index and worktree")
			}
			if n, err := restoreWorkspace(snap); err != nil || n != 0 {
				t.Fatalf("second restore = %d, %v; want 0, nil", n, err)
			}
		})
	}
}

func TestWorkspaceRestorePreservesStagedDeletion(t *testing.T) {
	repo := rewindRepo(t)
	writeRewindFile(t, repo, "keep.txt", "keep\n")
	git(t, repo, "add", "keep.txt")
	git(t, repo, "commit", "-qm", "keep")
	git(t, repo, "rm", "a.txt")
	snap := snapshotWorkspace()
	if snap == "" {
		t.Fatal("snapshot failed")
	}
	writeRewindFile(t, repo, "a.txt", "agent recreated\n")
	git(t, repo, "add", "a.txt")
	if _, err := restoreWorkspace(snap); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo, "a.txt")); !os.IsNotExist(err) {
		t.Fatalf("deleted file survived rewind: %v", err)
	}
	if got := rewindGit(t, "status", "--porcelain"); got != "D  a.txt" {
		t.Fatalf("staged deletion was not restored: %q", got)
	}
}

func TestWorkspaceRestoreProtectsDirectoryCollision(t *testing.T) {
	for _, direction := range []string{"file-to-directory", "directory-to-file"} {
		t.Run(direction, func(t *testing.T) {
			repo := rewindRepo(t)
			if direction == "directory-to-file" {
				git(t, repo, "rm", "a.txt")
				writeRewindFile(t, repo, "a.txt/nested.txt", "original\n")
				git(t, repo, "add", "-A")
				git(t, repo, "commit", "-qm", "directory")
			}
			snap := snapshotWorkspace()
			if snap == "" {
				t.Fatal("snapshot failed")
			}
			git(t, repo, "rm", "-r", "a.txt")
			name := "a.txt/nested.txt"
			if direction == "directory-to-file" {
				name = "a.txt"
			}
			writeRewindFile(t, repo, name, "keep user data\n")
			if _, err := restoreWorkspace(snap); err == nil {
				t.Fatal("expected directory collision error")
			}
			got, err := os.ReadFile(filepath.Join(repo, name))
			if err != nil || string(got) != "keep user data\n" {
				t.Fatalf("untracked content changed: %q, %v", got, err)
			}
		})
	}
}

func TestRewindSessionSaveFailurePreservesState(t *testing.T) {
	repo := rewindRepo(t)
	m := rewindModel(t, ai.Message{Role: "user", Content: "q1", Authored: true})
	snap := snapshotWorkspace()
	if snap == "" {
		t.Fatal("snapshot failed")
	}
	m.snapshots = map[int]string{1: snap}
	writeRewindFile(t, repo, "a.txt", "agent edit\n")
	m.store.Close()
	if _, ok := m.applyRewind(1); ok {
		t.Fatal("expected session save failure")
	}
	if len(m.agent.Messages) != 2 || len(m.future) != 0 || m.snapshots[1] != snap {
		t.Fatal("session save failure discarded conversation or snapshot")
	}
	rewindGit(t, "rev-parse", "--verify", "refs/k-brain/snapshots/"+snap)
}

func TestWorkspaceRestoreEmptyTree(t *testing.T) {
	repo := rewindRepo(t)
	git(t, repo, "rm", "a.txt")
	git(t, repo, "commit", "-qm", "empty")
	snap := snapshotWorkspace()
	if snap == "" {
		t.Fatal("empty committed tree should support snapshots")
	}
	if n, err := restoreWorkspace(snap); err != nil || n != 0 {
		t.Fatalf("empty restore = %d, %v", n, err)
	}
	writeRewindFile(t, repo, "added.txt", "agent edit\n")
	git(t, repo, "add", "added.txt")
	if n, err := restoreWorkspace(snap); err != nil || n != 1 {
		t.Fatalf("restore to empty tree = %d, %v", n, err)
	}
	if _, err := os.Stat(filepath.Join(repo, "added.txt")); !os.IsNotExist(err) {
		t.Fatalf("addition survived rewind: %v", err)
	}
}
