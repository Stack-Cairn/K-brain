package permissions_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/permissions"
	"github.com/Stack-Cairn/K-brain/internal/tools"
	"github.com/Stack-Cairn/K-brain/internal/tools/bashrun"
)

// runReal runs command through the same bashrun path the bash tool uses and reports
// whether it created the directory "pwned" in a fresh working directory. ok is false
// when the shell is not installed.
func runReal(t *testing.T, shell, command string) (created bool, output string, ok bool) {
	t.Helper()
	if _, err := exec.LookPath(shell); err != nil {
		return false, "", false
	}
	dir := t.TempDir()
	res := bashrun.Run(context.Background(), bashrun.Options{Command: command, Shell: shell, Dir: dir, Timeout: 60 * time.Second})
	_, err := os.Stat(filepath.Join(dir, "pwned"))
	return err == nil, res.Output + res.Exit, true
}

func ruleSet(effect permissions.Effect, rules ...string) permissions.RuleSet {
	set := permissions.RuleSet{Scope: permissions.ScopeUser}
	for _, r := range rules {
		set.Rules = append(set.Rules, permissions.StoredRule{Rule: r, Effect: effect})
	}
	return set
}

// Review P1: SplitCommand returns one covered segment for commands in which the real
// shell runs a second, uncovered command (`mkdir pwned`).
func TestReviewSplitMergesSeparateCommands(t *testing.T) {
	cases := []struct {
		name      string
		reqShell  string // what the gate passes to Evaluate ("" = TUI/ACP today)
		realShell string // what actually runs it
		command   string
	}{
		{"unknown/single-quoted double quote, bash", "", "bash", `echo safe '"'; mkdir pwned; echo '"'`},
		{"unknown/single-quoted double quote, pwsh", "", "pwsh", `echo safe '"'; mkdir pwned; echo '"'`},
		{"unknown/caret-escaped quote, cmd", "", "cmd", `echo safe ^" & mkdir pwned & echo ^"`},
		{"unknown/bare CR, pwsh", "", "pwsh", "echo safe\rmkdir pwned"},
		{"pwsh/bare CR", "pwsh", "pwsh", "echo safe\rmkdir pwned"},
		{"pwsh/typographic single quotes", "pwsh", "pwsh", "echo safe ‘\"’; mkdir pwned; echo ‘\"’"},
		{"pwsh/typographic double quotes", "pwsh", "pwsh", "echo safe “'”; mkdir pwned; echo “'”"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			evidence := "real shell not installed"
			if created, _, ok := runReal(t, tc.realShell, tc.command); ok {
				evidence = "real " + tc.realShell + " created pwned=" + boolText(created)
			}
			segs, ok := permissions.SplitCommand(tc.command, tc.reqShell)
			d := permissions.Evaluate(
				permissions.Request{Tool: "bash", Command: tc.command, Shell: tc.reqShell},
				permissions.Options{},
				ruleSet(permissions.Allow, "Bash(echo safe:*)"),
			)
			if d.Effect == permissions.Allow {
				t.Errorf("rule Bash(echo safe:*) allowed %q (segments=%q ok=%v); %s", tc.command, segs, ok, evidence)
			}
			if permissions.IsReadOnlyCommand(tc.command, tc.reqShell) {
				t.Errorf("%q treated as read-only; %s", tc.command, evidence)
			}
		})
	}
}

func boolText(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// Review P2: `>&word` with a non-numeric word writes stdout and stderr to the file
// `word` in bash, yet it is accepted as a descriptor duplication.
func TestReviewDupRedirectToFileIsNotReadOnly(t *testing.T) {
	command := "ls >&1pwned"
	evidence := "bash not installed"
	if _, err := exec.LookPath("bash"); err == nil {
		dir := t.TempDir()
		bashrun.Run(context.Background(), bashrun.Options{Command: command, Shell: "bash", Dir: dir, Timeout: 30 * time.Second})
		_, statErr := os.Stat(filepath.Join(dir, "1pwned"))
		evidence = "real bash wrote file 1pwned=" + boolText(statErr == nil)
	}
	for _, shell := range []string{"bash", ""} {
		if permissions.IsReadOnlyCommand(command, shell) {
			t.Errorf("shell %q: %q is read-only; %s", shell, command, evidence)
		}
	}
}

// Review P2: shell keywords make the first word of a segment, so Suggest hides the
// removal behind `then`, and the remembered keyword rules cover any later body.
func TestReviewShellKeywordSegments(t *testing.T) {
	root := t.TempDir()
	opts := permissions.Options{ProjectRoot: root}
	if got := permissions.Suggest(permissions.Request{Tool: "Bash", Command: "if true; then rm -rf build; fi", Shell: "bash"}, opts); got != nil {
		t.Errorf("a removal inside if/then is offered as always-allow: %q", got)
	}
	benign := permissions.Request{Tool: "Bash", Command: "if [ -f go.mod ]; then go build ./...; fi", Shell: "bash"}
	suggested := permissions.Suggest(benign, opts)
	set := ruleSet(permissions.Allow, suggested...)
	for _, cmd := range []string{"if true; then mkdir pwned; fi"} {
		evidence := "bash not installed"
		if created, _, ok := runReal(t, "bash", cmd); ok {
			evidence = "real bash created pwned=" + boolText(created)
		}
		d := permissions.Evaluate(permissions.Request{Tool: "Bash", Command: cmd, Shell: "bash"}, permissions.Options{ProjectRoot: root, AllowReadOnly: true}, set)
		if d.Effect == permissions.Allow {
			t.Errorf("remembering %q (rules %q) allows %q; %s", benign.Command, suggested, cmd, evidence)
		}
	}
	// `then`/`do` are not commands; a deny on rm must still see through them.
	deny := ruleSet(permissions.Deny, "Bash(rm:*)")
	if d := permissions.Evaluate(permissions.Request{Tool: "Bash", Command: "if true; then rm -rf build; fi", Shell: "bash"}, permissions.Options{}, deny); d.Effect != permissions.Deny {
		t.Errorf("deny Bash(rm:*) does not apply to rm after then: %+v", d)
	}
}

// Review P2: Suggest escapes glob characters with a backslash, ParseRule turns every
// backslash into a slash, so the remembered rule for a Next.js `[id]` route never
// covers the file it was created for.
func TestReviewSuggestedEditRuleCoversBracketPath(t *testing.T) {
	root := t.TempDir()
	req := permissions.Request{Tool: "Edit", Command: filepath.Join(root, "app", "[id]", "page.tsx")}
	suggested := permissions.Suggest(req, permissions.Options{ProjectRoot: root})
	if len(suggested) != 1 {
		t.Fatalf("suggested=%q", suggested)
	}
	if d := permissions.Evaluate(req, permissions.Options{ProjectRoot: root}, ruleSet(permissions.Allow, suggested...)); d.Effect != permissions.Allow {
		t.Errorf("suggested %q does not cover %s", suggested, req.Command)
	}
	stored, err := permissions.AddRule(filepath.Join(t.TempDir(), "rules.json"), "", permissions.StoredRule{Rule: suggested[0], Effect: permissions.Allow})
	if err != nil {
		t.Fatal(err)
	}
	if stored.Rule != suggested[0] {
		t.Errorf("AddRule rewrote %q to %q", suggested[0], stored.Rule)
	}
}

// Review P2: an old TUI rule `bash:kubectl` only matched the bare command (CommandRule
// of `kubectl delete ...` is `kubectl delete`); the import widens it to every kubectl
// command.
func TestReviewLegacyBareCommandNotWidened(t *testing.T) {
	home := t.TempDir()
	t.Setenv("K_BRAIN_HOME", home)
	data, _ := json.Marshal([]string{"bash:kubectl"})
	if err := os.WriteFile(filepath.Join(home, "permissions.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := "kubectl delete namespace prod"
	oldCovered := "bash:"+tools.CommandRule(cmd) == "bash:kubectl"
	set, err := permissions.LoadUserRules()
	if err != nil {
		t.Fatal(err)
	}
	d := permissions.Evaluate(permissions.Request{Tool: "bash", Command: cmd}, permissions.Options{}, set)
	if d.Effect == permissions.Allow && !oldCovered {
		t.Errorf("legacy bash:kubectl did not cover %q before (CommandRule=%q) but now allows it via %s", cmd, tools.CommandRule(cmd), d.Rule)
	}
}

// Review P2: plainSegment rejects `bash -c` and `pwsh -c`, but Suggest still offers an
// interpreter-wide prefix, and the -c filter misses forms the interpreters accept.
func TestReviewInterpreterRulesRunInlineCode(t *testing.T) {
	opts := permissions.Options{ProjectRoot: t.TempDir()}
	for _, tc := range []struct {
		approved, shell, later string
	}{
		{"bash scripts/build.sh", "bash", "echo 'mkdir pwned' | bash"},
		{"pwsh -File build.ps1", "pwsh", "pwsh -NoProfile –Command 'mkdir pwned'"},
		{"cmd /d build.bat", "cmd", "cmd /d /cmkdir pwned"},
		{"node build.js", "bash", "node -e \"require('fs').mkdirSync('pwned')\""},
	} {
		suggested := permissions.Suggest(permissions.Request{Tool: "Bash", Command: tc.approved, Shell: tc.shell}, opts)
		d := permissions.Evaluate(permissions.Request{Tool: "Bash", Command: tc.later, Shell: tc.shell}, permissions.Options{AllowReadOnly: true}, ruleSet(permissions.Allow, suggested...))
		evidence := "not run"
		if created, _, ok := runReal(t, tc.shell, tc.later); ok {
			evidence = "real " + tc.shell + " created pwned=" + boolText(created)
		}
		if d.Effect == permissions.Allow {
			t.Errorf("always-allow on %q stores %q, which then allows %q; %s", tc.approved, suggested, tc.later, evidence)
		}
	}
}

// Review P2 (Windows): a reader holding the rules file open makes the rename in
// saveFile fail, and the TUI drops that error.
func TestReviewAddRuleWhileReading(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("rename-over-open-file sharing violation is Windows behaviour")
	}
	path := filepath.Join(t.TempDir(), "rules.json")
	if _, err := permissions.AddRule(path, "", permissions.StoredRule{Rule: "Bash(go test:*)", Effect: permissions.Allow}); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_, _ = permissions.LoadRuleSet(path, permissions.ScopeUser)
				}
			}
		}()
	}
	var failures []string
	for i := range 200 {
		rule := "Bash(tool" + strings.Repeat("x", i%7) + string(rune('a'+i%26)) + " run" + string(rune('a'+i/26)) + ":*)"
		if _, err := permissions.AddRule(path, "", permissions.StoredRule{Rule: rule, Effect: permissions.Allow}); err != nil {
			failures = append(failures, err.Error())
		}
	}
	close(stop)
	wg.Wait()
	if len(failures) > 0 {
		t.Errorf("%d/200 AddRule calls failed while another goroutine read the file; first: %s", len(failures), failures[0])
	}
}

// Verification (passes): 16 concurrent AddRule calls in one process keep every rule.
func TestReviewConcurrentAddRuleKeepsAll(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.json")
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := permissions.AddRule(path, "", permissions.StoredRule{Rule: "Bash(task" + string(rune('a'+i)) + ":*)", Effect: permissions.Allow})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	set, err := permissions.LoadRuleSet(path, permissions.ScopeUser)
	if err != nil || len(set.Rules) != 16 {
		t.Fatalf("rules=%d err=%v, want 16", len(set.Rules), err)
	}
}
