package permissions

import "testing"

// KB-3 review, round 4 guard: common reads with globs stay read-only.
func TestReviewKB3R4GlobGuard(t *testing.T) {
	for _, c := range []struct{ command, shell string }{
		{"cat src/*/mod.rs", "bash"},
		{"ls crates/*/Cargo.toml", "bash"},
		{"ls *.go", "bash"},
		{"rg -g '*.rs' TODO", "bash"},
		{"find . -name '*.go'", "bash"},
		{"Get-ChildItem -Recurse -Filter *.md", "pwsh"},
		{"cd crates && ls *.toml", "bash"},
	} {
		d := Evaluate(Request{Tool: "Bash", Command: c.command, Shell: c.shell}, Options{AllowReadOnly: true})
		if d.Effect != Allow {
			t.Errorf("Evaluate(%q, %s) = %+v, want read-only allow", c.command, c.shell, d)
		}
	}
}

// Observations (not failures): conservative prompts the current rules cause.
func TestReviewKB3R4ConservativePrompts(t *testing.T) {
	for _, c := range []struct{ command, shell string }{
		{"ls */README.md", "bash"},
		{"rg os.environ src", "bash"},
		{"ls src/process/*.go", "bash"},
		{"cd src/process && cat main.go", "bash"},
		{"Get-ChildItem */package.json", "pwsh"},
	} {
		d := Evaluate(Request{Tool: "Bash", Command: c.command, Shell: c.shell}, Options{AllowReadOnly: true})
		t.Logf("Evaluate(%q, %s) allow=%v", c.command, c.shell, d.Effect == Allow)
	}
}
