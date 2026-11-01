package backend

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/protocol"
)

// KB-3 review, P1: the docs and IsReadOnlyCommand say git after cd is not read-only,
// because git reads that repository's config and core.fsmonitor runs a program. The
// backend decides with permissions.Evaluate, which never applies that rule: in normal
// mode `cd vendor-evil; git status` runs without a prompt and the hook runs.
func TestReviewKB3CdIntoOtherRepoThenGitRunsItsHookWithoutPrompt(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	command := "cd vendor-evil && git status --short"
	if runtime.GOOS == "windows" {
		if _, err := exec.LookPath("pwsh"); err != nil {
			t.Skip("pwsh not on PATH")
		}
		t.Setenv("K_BRAIN_SHELL", "pwsh.exe")
		command = "cd vendor-evil; git status --short"
	} else {
		t.Setenv("K_BRAIN_SHELL", "bash")
	}
	base, _ := newRulesServer(t, &rulesClient{batches: [][]string{{command}}})
	sess := createTestSession(t, base)

	// A repository inside the workspace that the user did not set up (a cloned
	// dependency, a fixture), whose config names a program for git to run.
	evil := filepath.Join(sess.CWD, "vendor-evil")
	if err := os.MkdirAll(evil, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"config", "core.fsmonitor", "echo pwned >fsmonitor-ran.txt; exit 1;"}} {
		cmd := exec.Command(git, args...)
		cmd.Dir = evil
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	events, prompts := runRules(t, base, sess.ID, "normal", func(protocol.PermissionRequest) protocol.PermissionDecision {
		return protocol.PermissionDecision{Decision: "reject"}
	})
	_, statErr := os.Stat(filepath.Join(evil, "fsmonitor-ran.txt"))
	if len(prompts) == 0 {
		t.Fatalf("%q ran without a prompt (auto_resolved=%+v, hook ran=%v); git after cd must ask", command, autoResolved(events), statErr == nil)
	}
	if statErr == nil {
		t.Fatalf("the rejected command still ran the repository's core.fsmonitor program")
	}
}
