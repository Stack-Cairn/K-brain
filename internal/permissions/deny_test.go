package permissions

import "testing"

// A deny rule names a program, so it must hold however the program is spelled or
// where its subcommand sits; over-denying only costs a prompt.
func TestDenyRuleSeesThroughSpelling(t *testing.T) {
	rules := deny("Bash(git push:*)")
	for _, cmd := range []string{
		`git push`,
		"/usr/bin/git push origin",
		"GIT.EXE push",
		"git -C .. push origin",
		"git --no-pager -c core.x=y push",
		"command git push",
		"git push > push.log",
		`g\it push`,
		`"git" push`,
	} {
		if d := Evaluate(Request{Tool: "Bash", Command: cmd, Shell: "bash"}, Options{}, rules); d.Effect != Deny {
			t.Errorf("%q: %+v, want deny", cmd, d)
		}
	}
	// PowerShell keeps backslashes, so a Windows path names the program there.
	for _, cmd := range []string{`C:\Git\cmd\git.exe push`, `& 'C:\Git\git.exe' push`} {
		if d := Evaluate(Request{Tool: "Bash", Command: cmd, Shell: "pwsh"}, Options{}, rules); d.Effect != Deny {
			t.Errorf("pwsh %q: %+v, want deny", cmd, d)
		}
	}
	for _, cmd := range []string{"git status", "git log --oneline", "echo git push"} {
		if d := Evaluate(Request{Tool: "Bash", Command: cmd, Shell: "bash"}, Options{}, rules); d.Effect == Deny {
			t.Errorf("%q must not be denied: %+v", cmd, d)
		}
	}
}

// Allow rules stay strict: a path or a reordered subcommand is not the approved command.
func TestAllowRuleIsStrictAboutSpelling(t *testing.T) {
	rules := allow("Bash(git push:*)")
	for _, cmd := range []string{"/tmp/git push", "git -C .. push"} {
		if d := Evaluate(Request{Tool: "Bash", Command: cmd, Shell: "bash"}, Options{}, rules); d.Effect == Allow {
			t.Errorf("%q must not be allowed: %+v", cmd, d)
		}
	}
}
