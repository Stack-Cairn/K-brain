package tui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func TestWorkspaceRewind(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init", "-q")
	git(t, repo, "config", "user.email", "t@t")
	git(t, repo, "config", "user.name", "t")
	git(t, repo, "config", "core.autocrlf", "false")
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("base\n"), 0o644)
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-qm", "base")
	t.Chdir(repo)

	m := compactCmdModel()
	st, err := session.Open(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	m.store = st
	m.sessionID, _ = st.Create(repo, m.modelName, m.provName)

	m.snapshots = map[int]string{}
	snap := snapshotWorkspace()
	if snap == "" {
		t.Fatal("a clean tree still snapshots (as HEAD) — the point is pre-turn state")
	}
	if !workspaceClean() {
		t.Fatal("tree should be clean before the turn")
	}
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("agent edit\n"), 0o644)
	if workspaceClean() {
		t.Fatal("tree should be dirty after the agent's edit")
	}

	m.snapshots[3] = snap
	if err := st.SetSnapshot(m.sessionID, 3, snap); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(repo, "mine.txt"), []byte("keep me\n"), 0o644)

	m.agent.Messages = append(m.agent.Messages,
		ai.Message{Role: "system"},
		ai.Message{Role: "user", Content: "q1", Authored: true},
		ai.Message{Role: "assistant", Content: "a1"},
		ai.Message{Role: "user", Content: "do the edit", Authored: true},
		ai.Message{Role: "assistant", Content: "done"},
	)
	m.rebuildTranscript()
	m.applyRewind(3)

	body, _ := os.ReadFile(filepath.Join(repo, "a.txt"))
	if string(body) != "base\n" {
		t.Fatalf("tracked file not restored: %q", body)
	}
	if _, err := os.Stat(filepath.Join(repo, "mine.txt")); err != nil {
		t.Fatal("untracked user file must survive a workspace rewind")
	}
	if got := st.Snapshots(m.sessionID); len(got) != 0 {
		t.Fatalf("consumed snapshot rows should be trimmed, got %v", got)
	}

	var sawNote bool
	for _, b := range m.blocks {
		if strings.Contains(ansi.Strip(b.render(m.width)), "workspace rewound") {
			sawNote = true
		}
	}
	if !sawNote {
		t.Fatal("transcript should record the workspace rewind")
	}
}

func TestWorkspaceRewindNoSnapshot(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init", "-q")
	git(t, repo, "config", "user.email", "t@t")
	git(t, repo, "config", "user.name", "t")
	git(t, repo, "config", "core.autocrlf", "false")
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("base\n"), 0o644)
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-qm", "base")
	t.Chdir(repo)

	m := compactCmdModel()
	m.snapshots = map[int]string{}
	m.agent.Messages = append(m.agent.Messages,
		ai.Message{Role: "system"},
		ai.Message{Role: "user", Content: "q1", Authored: true},
		ai.Message{Role: "assistant", Content: "a1"},
	)
	m.rebuildTranscript()
	blocksBefore := len(m.blocks)
	m.applyRewind(1)
	for _, b := range m.blocks {
		if strings.Contains(ansi.Strip(b.render(m.width)), "workspace rewound") {
			t.Fatal("no snapshot, no rewind note")
		}
	}
	if len(m.blocks) >= blocksBefore {
		t.Fatal("rewind should still clip the transcript")
	}
	body, _ := os.ReadFile(filepath.Join(repo, "a.txt"))
	if string(body) != "base\n" {
		t.Fatalf("file should be untouched: %q", body)
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}
