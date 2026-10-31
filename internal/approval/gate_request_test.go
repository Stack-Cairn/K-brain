package approval

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/tools"
	"github.com/Stack-Cairn/K-brain/internal/tools/bashrun"
)

// When a review sends a call to manual approval, the gate must see the command and
// shell the tool itself would check, not the raw JSON arguments; otherwise remembered
// permission rules can never match and the prompt shows JSON.
func TestReviewGateSeesToolCommandAndShell(t *testing.T) {
	root := t.TempDir()
	base := tools.WithWorkingDir(t.Context(), root)
	medium := `{"risk":"medium","sandbox":"low","sensitive_data":"low","model_behavior":"low","injection":"low","reason":"needs a person"}`
	for _, tc := range []struct {
		tool, args, command, shell string
	}{
		{"bash", `{"command":"go version; Remove-Item x","shell":"pwsh"}`, "go version; Remove-Item x", "pwsh"},
		{"bash", `{"command":"go version"}`, "go version", bashrun.ResolveShell(bashrun.WithShell(base, ""), "")},
		{"Bash", `{"command":"npm test","shell":"cmd"}`, "npm test", "cmd"},
		{"write", `{"path":"notes.txt","content":"x"}`, filepath.Join(root, "notes.txt"), ""},
		{"mcp__srv__tool", `{"q":1}`, `{"q":1}`, ""},
	} {
		var got tools.GateRequest
		run := NewRun("auto", "do it", fake(medium), func(string, string, Report, bool) {})
		hook := run.Hooks(func(req tools.GateRequest) (tools.GateDecision, string) {
			got = req
			return tools.GateAllowOnce, ""
		})
		if _, err := hook.Before(base, tc.tool, json.RawMessage(tc.args)); err != nil {
			t.Fatalf("%s: %v", tc.tool, err)
		}
		if got.Tool != tc.tool || got.Command != tc.command || got.Shell != tc.shell {
			t.Errorf("%s %s: gate saw tool=%q command=%q shell=%q, want %q %q", tc.tool, tc.args, got.Tool, got.Command, got.Shell, tc.command, tc.shell)
		}
		if got.Rule != tools.CommandRule(tc.command) {
			t.Errorf("%s: rule=%q, want %q", tc.tool, got.Rule, tools.CommandRule(tc.command))
		}
		if got.Description == "" || got.Context == nil {
			t.Errorf("%s: review description and context must still reach the gate: %+v", tc.tool, got)
		}
	}
}
