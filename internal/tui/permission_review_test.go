package tui

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/permissions"
	"github.com/Stack-Cairn/K-brain/internal/tools"
	"github.com/Stack-Cairn/K-brain/internal/tools/bashrun"
)

func reviewDecide(t *testing.T, m *model, tool, command string) (prompted bool, ans permAnswer) {
	t.Helper()
	m.permDialog = nil
	reply := make(chan permAnswer, 1)
	m.receivePermission(permRequest{req: tools.GateRequest{Context: t.Context(), Tool: tool, Command: command, Rule: tools.CommandRule(command)}, reply: reply})
	if m.permDialog != nil {
		return true, permAnswer{}
	}
	return false, <-reply
}

// Review P1: with no rules at all, normal mode runs these without a prompt because
// the strict parser sees a single read-only `echo`; bash/pwsh run Remove-Item/rm.
func TestReviewQuoteMixRunsUnpromptedInNormalMode(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	m := &model{permissionMode: "normal"}
	for _, cmd := range []string{
		`echo '"'; Remove-Item -Recurse -Force src; echo '"'`,
		`echo '"'; rm -rf ~; echo '"'`,
		"echo ok\rRemove-Item -Recurse -Force src",
	} {
		if prompted, ans := reviewDecide(t, m, "bash", cmd); !prompted {
			t.Errorf("normal mode, no rules: %q ran without a prompt (decision=%v)", cmd, ans.decision)
		}
	}
}

// Review P1: "deny rules also apply in always/full_access" holds only for commands the
// splitter accepts; a redirect, comment or wrapper skips the deny entirely.
func TestReviewDenyRuleSurvivesUnparsableCommand(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	for _, mode := range []string{"always", "full_access"} {
		m := &model{permissionMode: mode, perms: permRules{user: permissions.RuleSet{Scope: permissions.ScopeUser, Rules: []permissions.StoredRule{{Rule: "Bash(git push:*)", Effect: permissions.Deny}}}}}
		for _, cmd := range []string{
			"git push origin main > push.log",
			"git push origin main # ship it",
			"env git push origin main",
			"(git push origin main)",
		} {
			prompted, ans := reviewDecide(t, m, "bash", cmd)
			if prompted || ans.decision != tools.GateReject {
				t.Errorf("%s mode, deny Bash(git push:*): %q -> prompted=%v decision=%v, want reject", mode, cmd, prompted, ans.decision)
			}
		}
	}
}

// Review P2 (Windows): auto_edit lets the model write git.bat without a prompt, and the
// new read-only pass then lets `git status` run without a prompt. The bash tool takes
// a shell override the gate does not see; cmd.exe resolves `git` from the working
// directory first, so the written batch file runs.
func TestReviewReadOnlyCommandRunsWorkingDirExecutableUnderCmd(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("cmd.exe current-directory lookup")
	}
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	// A stock Windows install does not define this; some agent harnesses do, which
	// hides cmd.exe's current-directory lookup.
	if old, ok := os.LookupEnv("NoDefaultCurrentDirectoryInExePath"); ok {
		_ = os.Unsetenv("NoDefaultCurrentDirectoryInExePath")
		t.Cleanup(func() { _ = os.Setenv("NoDefaultCurrentDirectoryInExePath", old) })
	}
	m := &model{permissionMode: "auto_edit"}
	dir := t.TempDir()
	if prompted, ans := reviewDecide(t, m, "write", filepath.Join(dir, "git.bat")); prompted || ans.decision == tools.GateReject {
		t.Fatalf("precondition: auto_edit allows the write without a prompt")
	}
	if err := os.WriteFile(filepath.Join(dir, "git.bat"), []byte("@mkdir pwned\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	prompted, ans := reviewDecide(t, m, "bash", "git status")
	res := bashrun.Run(context.Background(), bashrun.Options{Command: "git status", Shell: "cmd", Dir: dir, Timeout: 30 * time.Second})
	_, err := os.Stat(filepath.Join(dir, "pwned"))
	if !prompted && ans.decision != tools.GateReject && err == nil {
		t.Errorf("auto_edit: `git status` allowed without a prompt and cmd ran the written git.bat (output %q)", res.Output)
	}
}
