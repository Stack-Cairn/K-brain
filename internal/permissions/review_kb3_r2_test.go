package permissions

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// KB-3 review, round 2. All cases go through Evaluate/Suggest.

// P2: git after cd is no longer read-only, so Suggest now offers Bash(git status:*)
// for `cd vendor-a && git status`. A bare `git status` is read-only anyway, so the
// remembered rule adds exactly one thing: git after a cd into any directory. It is a
// command rule, stored in the user scope, so it applies in every project.
func TestReviewKB3R2SuggestAfterCdDoesNotRememberGitEverywhere(t *testing.T) {
	req := Request{Tool: "Bash", Command: "cd vendor-a && git status", Shell: "bash"}
	rules := Suggest(req, Options{AllowReadOnly: true})
	for _, rule := range rules {
		set := RuleSet{Scope: DefaultScope(rule), Rules: []StoredRule{{Rule: rule, Effect: Allow}}}
		other := Request{Tool: "Bash", Command: "cd /some/other/clone && git status", Shell: "bash"}
		d := Evaluate(other, Options{AllowReadOnly: true, ProjectRoot: "/another/project"}, set)
		if d.Effect == Allow {
			t.Errorf("Suggest(%q) offers %s (scope %s); once remembered, %q in another project runs without a prompt", req.Command, rule, set.Scope, other.Command)
		}
	}
}

// P2: the /proc environ check looks for "/proc/" and "environ" in one word. A shell
// glob or a cd splits them, and bash still reads the environment.
func TestReviewKB3R2ProcEnvironBypass(t *testing.T) {
	commands := []string{
		"cat /proc/self/env*",
		"cat /proc/self/e?viron",
		"cd /proc/self && cat environ",
		"grep -a K_BRAIN_REVIEW_SECRET /proc/self/env*",
	}
	if bash, err := exec.LookPath("bash"); err == nil {
		probe := exec.Command(bash, "-c", "test -r /proc/self/environ")
		if probe.Run() == nil {
			const secret = "kb3-review-r2-s3cr3t"
			for _, command := range commands {
				cmd := exec.Command(bash, "-c", command)
				cmd.Env = append(os.Environ(), "K_BRAIN_REVIEW_SECRET="+secret)
				out, _ := cmd.CombinedOutput()
				if !strings.Contains(string(out), secret) {
					t.Fatalf("expected %q to print the environment, got %.200q", command, out)
				}
			}
		} else {
			t.Log("bash has no readable /proc/self/environ; behavior proof skipped")
		}
	}
	for _, command := range commands {
		d := Evaluate(Request{Tool: "Bash", Command: command, Shell: "bash"}, Options{AllowReadOnly: true})
		if d.Effect == Allow && d.Source == SourceReadOnly {
			t.Errorf("Evaluate(%q) = read-only allow; it prints the process environment", command)
		}
	}
}

// P2: the ps rule only catches "env" and BSD-style lowercase e. macOS/BSD ps(1):
// "-E  Display the environment as well." (dash form, capital E).
func TestReviewKB3R2PsDashCapitalEPrintsEnvironment(t *testing.T) {
	for _, command := range []string{"ps -E", "ps -axE", "ps -wwE -p 1"} {
		d := Evaluate(Request{Tool: "Bash", Command: command, Shell: "bash"}, Options{AllowReadOnly: true})
		if d.Effect == Allow && d.Source == SourceReadOnly {
			t.Errorf("Evaluate(%q) = read-only allow; BSD ps -E prints each process's environment", command)
		}
	}
}

// Guard (passes today): searches for "::" stay read-only, provider paths do not.
func TestReviewKB3R2DoubleColonGuard(t *testing.T) {
	for _, c := range []struct {
		command, shell string
		want           bool
	}{
		{"rg std::io src", "bash", true},
		{`grep -rn "Foo::bar" .`, "bash", true},
		{"Select-String -Pattern Foo::Bar -Path src/lib.rs", "pwsh", true},
		{"cat src/a::b", "bash", true},
		{"git log origin/main..HEAD --oneline", "bash", true},
		{"git log --format=%h::%s", "bash", true},
		{"Get-Content -Path:Environment::OS", "pwsh", false},
		{`Get-Content microsoft.powershell.core\REGISTRY::HKCU\x`, "pwsh", false},
		{"Get-ChildItem -LiteralPath:WSMan::localhost", "pwsh", false},
		{"Get-Item -Path:Cert:CurrentUser", "pwsh", false},
	} {
		d := Evaluate(Request{Tool: "Bash", Command: c.command, Shell: c.shell}, Options{AllowReadOnly: true})
		if got := d.Effect == Allow; got != c.want {
			t.Errorf("Evaluate(%q, %s) allow=%v, want %v", c.command, c.shell, got, c.want)
		}
	}
}
