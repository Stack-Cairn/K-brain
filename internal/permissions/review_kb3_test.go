package permissions

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// KB-3 review. Every case goes through Evaluate, the function the backend, TUI and
// ACP call; IsReadOnlyCommand is not on any production path.

var reviewReadOnly = Options{AllowReadOnly: true}

func reviewAllowsReadOnly(command, shell string) bool {
	d := Evaluate(Request{Tool: "Bash", Command: command, Shell: shell}, reviewReadOnly)
	return d.Effect == Allow && d.Source == SourceReadOnly
}

// P1: the cd-then-git restriction lives only in IsReadOnlyCommand. Evaluate checks
// readOnlySegment per segment, so `cd other-repo && git status` still runs without a
// prompt and git reads other-repo's config (core.fsmonitor runs a program).
func TestReviewKB3EvaluateCdThenGitIsNotReadOnly(t *testing.T) {
	for _, c := range []struct{ command, shell string }{
		{"cd crates && git status", "bash"},
		{"cd crates; git status --short", "pwsh"},
		{"Set-Location vendor; git diff", "pwsh"},
		{"pushd vendor && git log --oneline", "bash"},
	} {
		if IsReadOnlyCommand(c.command, c.shell) {
			t.Fatalf("precondition: IsReadOnlyCommand(%q) is false per the KB-3 design", c.command)
		}
		if reviewAllowsReadOnly(c.command, c.shell) {
			t.Errorf("Evaluate(%q, %s) = read-only allow; IsReadOnlyCommand says not read-only (git after cd reads that repo's config)", c.command, c.shell)
		}
	}
}

// P1: git stash list/show take log/diff options, including --output (writes a file)
// and --ext-diff (runs diff.external); diff/log/show already reject them.
func TestReviewKB3GitStashOutputIsNotReadOnly(t *testing.T) {
	if git, err := exec.LookPath("git"); err == nil {
		dir := t.TempDir()
		run := func(args ...string) {
			cmd := exec.Command(git, args...)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@b", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@b")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
		run("init", "-q")
		if err := os.WriteFile(filepath.Join(dir, "f"), []byte("a\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		run("add", "f")
		run("commit", "-qm", "i")
		if err := os.WriteFile(filepath.Join(dir, "f"), []byte("b\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		run("stash", "-q")
		run("stash", "show", "-p", "--output=written-by-show.txt")
		run("stash", "list", "--output=written-by-list.txt")
		for _, name := range []string{"written-by-show.txt", "written-by-list.txt"} {
			if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
				t.Fatalf("expected git to write %s: %v", name, err)
			}
		}
	}
	for _, command := range []string{
		"git stash show -p --output=leak.txt",
		"git stash list --output=leak.txt",
		"git stash show --ext-diff",
	} {
		if reviewAllowsReadOnly(command, "bash") {
			t.Errorf("Evaluate(%q) = read-only allow; it writes a file or runs diff.external", command)
		}
	}
}

// P2: providerPath only checks a word prefix and "registry::". PowerShell also takes
// -Param:Value in one word and provider-qualified paths (Environment::),
// so environment variables are read without a prompt.
func TestReviewKB3ProviderPathBypass(t *testing.T) {
	cases := []struct {
		command string
		secret  bool // the output contains the environment variable's value
	}{
		{"Get-Content -Path:Env:K_BRAIN_REVIEW_SECRET", true},
		{"gc -LiteralPath:Env:K_BRAIN_REVIEW_SECRET", true},
		{"Get-ChildItem -Path:Env: | Format-Table -AutoSize | Out-String -Width 4096", true},
		{"Get-Content Environment::K_BRAIN_REVIEW_SECRET", true},
		{`Get-ChildItem Microsoft.PowerShell.Core\Environment::K_BRAIN_REVIEW_SECRET | Select-Object -ExpandProperty Value`, true},
	}
	if pwsh, err := exec.LookPath("pwsh"); err == nil {
		const secret = "kb3-review-s3cr3t"
		for _, c := range cases {
			if !c.secret {
				continue
			}
			cmd := exec.Command(pwsh, "-NoProfile", "-NonInteractive", "-Command", c.command)
			cmd.Env = append(os.Environ(), "K_BRAIN_REVIEW_SECRET="+secret)
			out, _ := cmd.CombinedOutput()
			if !strings.Contains(string(out), secret) {
				t.Fatalf("expected %q to print the variable, got %s", c.command, out)
			}
		}
	}
	for _, c := range cases {
		if reviewAllowsReadOnly(c.command, "pwsh") {
			t.Errorf("Evaluate(%q, pwsh) = read-only allow; it reads a PowerShell provider drive", c.command)
		}
	}
}

// P2: "any program with only --version" includes PowerShell cmdlets, which take
// --version as a positional value: New-Item --version creates a file.
func TestReviewKB3VersionOnlyCmdletWrites(t *testing.T) {
	if pwsh, err := exec.LookPath("pwsh"); err == nil {
		dir := t.TempDir()
		cmd := exec.Command(pwsh, "-NoProfile", "-NonInteractive", "-Command", "New-Item --version | Out-Null")
		cmd.Dir = dir
		_, _ = cmd.CombinedOutput()
		if _, err := os.Stat(filepath.Join(dir, "--version")); err != nil {
			t.Fatalf("expected New-Item --version to create a file: %v", err)
		}
	}
	for _, command := range []string{"New-Item --version", "ni --version", "Out-File --version", "Remove-Item --version"} {
		if reviewAllowsReadOnly(command, "pwsh") {
			t.Errorf("Evaluate(%q, pwsh) = read-only allow; the cmdlet writes or deletes a file named --version", command)
		}
	}
}
