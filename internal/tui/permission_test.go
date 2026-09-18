package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Stack-Cairn/K-brain/internal/config"
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
	m.perms = permRules{}

	req := tools.GateRequest{Tool: "bash", Command: "git checkout main", Rule: "git checkout"}
	if m.perms.coveredBy(req) {
		t.Fatal("no rules yet — should not be covered")
	}

	reply := make(chan permAnswer, 1)
	m.permDialog = &permDialog{req: req, reply: reply}
	if got := m.permView(); !strings.Contains(got, "always allows: bash:git checkout") {
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

	if !m.perms.coveredBy(tools.GateRequest{Tool: "bash", Command: "git checkout other-branch", Rule: "git checkout"}) {
		t.Fatal("allow-always on 'git checkout' should cover other branches")
	}

	if reloaded := loadPermRules(); !reloaded[ruleKey("bash", "git checkout")] {
		t.Fatal("the rule should persist to permissions.json")
	}
	if _, err := os.Stat(filepath.Join(configDir(t), "permissions.json")); err != nil {
		t.Fatal("permissions.json should exist")
	}

	if m.perms.coveredBy(tools.GateRequest{Tool: "bash", Command: "rm -rf build", Rule: "rm"}) {
		t.Fatal("an unrelated command must still prompt")
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
