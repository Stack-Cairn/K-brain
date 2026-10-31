package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Stack-Cairn/K-brain/internal/approval"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/permissions"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func TestCommandRuleArity(t *testing.T) {
	cases := map[string]string{
		"git checkout main":             "git checkout",
		"git commit -m 'x'":             "git commit",
		"npm run dev":                   "npm run dev",
		"npm install lodash":            "npm install",
		"ls -la /tmp":                   "ls",
		"rm -rf build":                  "rm",
		"FOO=bar go test ./...":         "go test",
		"docker compose up":             "docker compose up",
		"git checkout main && rm -rf /": "git checkout",
		"somescript.sh --flag":          "somescript.sh",
	}
	for in, want := range cases {
		if got := tools.CommandRule(in); got != want {
			t.Errorf("CommandRule(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPermissionGateFlow(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	m := compactCmdModel()
	m.installPermGate()

	req := tools.GateRequest{Tool: "bash", Command: "git checkout main", Rule: "git checkout"}
	if m.perms.decide(req, false).Effect != "" {
		t.Fatal("no rules yet — should not be covered")
	}

	reply := make(chan permAnswer, 1)
	m.permDialog = &permDialog{req: req, reply: reply}
	if got := m.permView(); !strings.Contains(got, "always allows: Bash(git checkout:*)") {
		t.Fatalf("the dialog previews the rule it installs:\n%s", got)
	}
	m.permKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("A")})
	ans := <-reply
	if ans.decision != tools.GateAllowAlways {
		t.Fatalf("A should allow always, got %v", ans.decision)
	}
	if m.permDialog != nil {
		t.Fatal("the dialog should close on an answer")
	}

	other := tools.GateRequest{Tool: "bash", Command: "git checkout other-branch", Rule: "git checkout"}
	if m.perms.decide(other, false).Effect != permissions.Allow {
		t.Fatal("allow-always on 'git checkout' should cover other branches")
	}

	if reloaded := loadPermRules(); permissions.Evaluate(permissions.Request{Tool: "bash", Command: "git checkout x"}, permissions.Options{}, reloaded.user).Effect != permissions.Allow {
		t.Fatal("the rule should persist to the shared rules file")
	}
	if _, err := os.Stat(filepath.Join(configDir(t), "permission-rules.json")); err != nil {
		t.Fatal("permission-rules.json should exist")
	}

	if m.perms.decide(tools.GateRequest{Tool: "bash", Command: "rm -rf build", Rule: "rm"}, false).Effect != "" {
		t.Fatal("an unrelated command must still prompt")
	}
}

func TestAllowAlwaysOnDestructiveCommandAllowsOnce(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	m := compactCmdModel()
	m.installPermGate()
	req := tools.GateRequest{Tool: "bash", Command: "rm -rf build", Rule: "rm"}
	reply := make(chan permAnswer, 1)
	m.permDialog = &permDialog{req: req, reply: reply}
	if got := m.permView(); !strings.Contains(got, "always allow is not available") {
		t.Fatalf("the dialog says removals are not remembered:\n%s", got)
	}
	m.permKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("A")})
	if ans := <-reply; ans.decision != tools.GateAllowOnce {
		t.Fatalf("A on a removal allows once, got %v", ans.decision)
	}
	if m.perms.decide(req, false).Effect != "" {
		t.Fatal("a removal must not be remembered")
	}
}

func TestReadOnlyCommandSkipsPrompt(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	m := &model{permissionMode: "normal"}
	reply := make(chan permAnswer, 1)
	m.receivePermission(permRequest{req: tools.GateRequest{Context: t.Context(), Tool: "bash", Command: "git status", Shell: "bash"}, reply: reply})
	if m.permDialog != nil {
		t.Fatal("a read-only command runs without a prompt")
	}
	if ans := <-reply; ans.decision != tools.GateAllowOnce {
		t.Fatalf("decision=%v", ans.decision)
	}
}

func TestDenyRuleRejectsEvenInAlwaysMode(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	m := &model{permissionMode: "always", perms: permRules{user: permissions.RuleSet{Scope: permissions.ScopeUser, Rules: []permissions.StoredRule{{Rule: "Bash(git push:*)", Effect: permissions.Deny}}}}}
	reply := make(chan permAnswer, 1)
	m.receivePermission(permRequest{req: tools.GateRequest{Context: t.Context(), Tool: "bash", Command: "git push origin main"}, reply: reply})
	if ans := <-reply; ans.decision != tools.GateReject || !strings.Contains(ans.redirect, "Bash(git push:*)") {
		t.Fatalf("deny rule: %+v", ans)
	}
}

func TestPermissionRejectRedirect(t *testing.T) {
	m := compactCmdModel()
	m.perms = permRules{}
	reply := make(chan permAnswer, 1)
	m.permDialog = &permDialog{req: tools.GateRequest{Tool: "bash", Command: "rm x", Rule: "rm"}, reply: reply}

	m.permKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if !m.permDialog.rejecting {
		t.Fatal("r should switch to the redirect prompt")
	}
	for _, c := range "don't delete, archive it" {
		m.permKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{c}})
	}
	m.permKey(tea.KeyMsg{Type: tea.KeyEnter})
	ans := <-reply
	if ans.decision != tools.GateReject || ans.redirect != "don't delete, archive it" {
		t.Fatalf("reject should carry the redirect, got %+v", ans)
	}
}

func configDir(t *testing.T) string {
	t.Helper()
	dir, err := config.Dir()
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestAIApprovalNeverOverridesPlanAndUnknownRequiresManual(t *testing.T) {
	for _, tc := range []struct {
		mode, risk           string
		plan, manual, reject bool
	}{
		{"auto", "low", false, false, false},
		{"auto", "unknown", false, true, false},
		{"auto", "low", true, false, true},
		{"risk_only", "low", false, true, false},
	} {
		m := &model{cfg: &config.Config{AIApproval: config.AIApprovalConfig{Mode: tc.mode}}, perms: permRules{}}
		if tc.plan {
			m.permissionMode = "plan"
		}
		if tc.mode == "auto" && tc.risk == "unknown" {
			m.permissionMode = "auto_review"
		}
		report := approval.Report{Risk: tc.risk, Sandbox: tc.risk, SensitiveData: tc.risk, ModelBehavior: tc.risk, Injection: tc.risk, Reason: "review"}
		ctx := context.WithValue(t.Context(), approval.ActionReportKey{}, report)
		reply := make(chan permAnswer, 1)
		m.receivePermission(permRequest{req: tools.GateRequest{Context: ctx, Tool: "bash", Command: "pwd"}, reply: reply})
		if tc.manual {
			if m.permDialog == nil {
				t.Fatal("manual fallback missing")
			}
			continue
		}
		select {
		case answer := <-reply:
			if (answer.decision == tools.GateReject) != tc.reject {
				t.Fatalf("decision=%v", answer)
			}
		default:
			t.Fatal("no answer")
		}
	}
}

func TestPermissionModeDecisions(t *testing.T) {
	for _, tc := range []struct {
		mode, tool, risk string
		manual           bool
	}{
		{"normal", "bash", "", true},
		{"auto_edit", "write", "", false},
		{"auto_edit", "bash", "", true},
		{"auto_review", "bash", "low", false},
		{"auto_review", "bash", "unknown", true},
		{"auto_review", "bash", "", true},
		{"full_access", "bash", "", false},
		{"always", "bash", "unknown", false},
	} {
		t.Run(tc.mode+"/"+tc.tool+"/"+tc.risk, func(t *testing.T) {
			m := &model{permissionMode: tc.mode, perms: permRules{}}
			ctx := t.Context()
			if tc.risk != "" {
				report := approval.Report{Risk: tc.risk, Sandbox: tc.risk, SensitiveData: tc.risk, ModelBehavior: tc.risk, Injection: tc.risk}
				ctx = context.WithValue(ctx, approval.ActionReportKey{}, report)
			}
			reply := make(chan permAnswer, 1)
			m.receivePermission(permRequest{req: tools.GateRequest{Context: ctx, Tool: tc.tool}, reply: reply})
			if (m.permDialog != nil) != tc.manual {
				t.Fatalf("manual=%v", m.permDialog != nil)
			}
			if !tc.manual {
				select {
				case answer := <-reply:
					if answer.decision != tools.GateAllowOnce {
						t.Fatalf("decision=%v", answer.decision)
					}
				default:
					t.Fatal("missing automatic answer")
				}
			}
		})
	}
}

// Regression: remembering `git checkout` must not cover a compound command whose
// later segments were never approved.
func TestRememberedRuleDoesNotCoverCompoundCommand(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	m := compactCmdModel()
	m.installPermGate()
	reply := make(chan permAnswer, 1)
	m.permDialog = &permDialog{req: tools.GateRequest{Tool: "bash", Command: "git checkout main", Rule: "git checkout"}, reply: reply}
	m.permKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("A")})
	<-reply

	reply = make(chan permAnswer, 1)
	m.receivePermission(permRequest{req: tools.GateRequest{Context: t.Context(), Tool: "bash", Command: "git checkout x && rm -rf ~", Rule: "git checkout"}, reply: reply})
	if m.permDialog == nil {
		t.Fatal("a compound command with an unapproved segment must prompt")
	}
}

// A call the review gate already sent to the user is not asked about again by the
// tool's own gate, even when deny rules make an unsplittable command ask.
func TestReviewedCallIsNotAskedTwice(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	m := &model{permissionMode: "normal", perms: permRules{user: permissions.RuleSet{Scope: permissions.ScopeUser, Rules: []permissions.StoredRule{{Rule: "Bash(git push:*)", Effect: permissions.Deny}}}}}
	ctx := context.WithValue(t.Context(), approval.ReviewedActionKey{}, "bash")
	reply := make(chan permAnswer, 1)
	m.receivePermission(permRequest{req: tools.GateRequest{Context: ctx, Tool: "bash", Command: "make > build.log", Shell: "pwsh"}, reply: reply})
	if m.permDialog != nil {
		t.Fatal("the user already approved this call at the review gate")
	}
	if ans := <-reply; ans.decision != tools.GateAllowOnce {
		t.Fatalf("decision=%v", ans.decision)
	}
	// A deny rule still wins over an earlier approval.
	reply = make(chan permAnswer, 1)
	m.receivePermission(permRequest{req: tools.GateRequest{Context: ctx, Tool: "bash", Command: "git push > log", Shell: "pwsh"}, reply: reply})
	if ans := <-reply; ans.decision != tools.GateReject {
		t.Fatalf("deny after review approval: %v", ans.decision)
	}
}

// The prompt raised from the review gate shows the command and offers its rule.
func TestReviewPromptOffersCommandRule(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	m := &model{permissionMode: "normal", cfg: &config.Config{AIApproval: config.AIApprovalConfig{Mode: "auto"}}}
	report := approval.Report{Risk: "medium", Sandbox: "low", SensitiveData: "low", ModelBehavior: "low", Injection: "low", Reason: "check"}
	ctx := context.WithValue(t.Context(), approval.ActionReportKey{}, report)
	reply := make(chan permAnswer, 1)
	m.receivePermission(permRequest{req: tools.GateRequest{Context: ctx, Tool: "bash", Command: "go version", Shell: "pwsh"}, reply: reply})
	if m.permDialog == nil {
		t.Fatal("a medium-risk review asks the user")
	}
	if got := m.permView(); !strings.Contains(got, "always allows: Bash(go version:*)") {
		t.Fatalf("prompt:\n%s", got)
	}
}

// Remembering an edit stores a path inside this project, so it must not apply to the
// same relative path in other projects; command rules still apply everywhere.
func TestEditRuleIsRememberedForThisProjectOnly(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	projectA, projectB := t.TempDir(), t.TempDir()
	root := projectA
	old := currentRoot
	currentRoot = func() (string, error) { return root, nil }
	t.Cleanup(func() { currentRoot = old })

	m := compactCmdModel()
	m.installPermGate()
	for _, req := range []tools.GateRequest{
		{Tool: "edit", Command: filepath.Join(projectA, "sub", "main.go")},
		{Tool: "bash", Command: "go version", Shell: "pwsh"},
	} {
		reply := make(chan permAnswer, 1)
		m.permDialog = &permDialog{req: req, reply: reply}
		m.permKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("A")})
		if ans := <-reply; ans.decision != tools.GateAllowAlways {
			t.Fatalf("%s: A should remember, got %v", req.Tool, ans.decision)
		}
	}

	editA := tools.GateRequest{Tool: "edit", Command: filepath.Join(projectA, "sub", "other.go")}
	if m.perms.decide(editA, false).Effect != permissions.Allow {
		t.Fatal("the edit rule covers its own project")
	}
	user, _ := permissions.LoadUserRules()
	for _, r := range user.Rules {
		if r.Rule == "Edit(sub/**)" {
			t.Fatal("an edit rule must not be stored for every project")
		}
	}

	root = projectB
	fresh := loadPermRules()
	editB := tools.GateRequest{Tool: "edit", Command: filepath.Join(projectB, "sub", "main.go")}
	if fresh.decide(editB, false).Effect != "" || m.perms.decide(editB, false).Effect != "" {
		t.Fatal("an edit rule from project A must not cover project B")
	}
	if fresh.decide(tools.GateRequest{Tool: "bash", Command: "go version", Shell: "pwsh"}, false).Effect != permissions.Allow {
		t.Fatal("command rules apply in every project")
	}
	root = projectA
	reloaded := loadPermRules()
	if reloaded.decide(editA, false).Effect != permissions.Allow {
		t.Fatal("the project rule persists for project A")
	}
}
