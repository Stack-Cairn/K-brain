package tui

import (
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/permissions"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// The question tool only asks the user, so no mode prompts for it (auto_review sees it
// without a review report, plan mode keeps it); a deny rule for it still refuses.
func TestQuestionToolNeedsNoApprovalButDenyWins(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	args := `{"question":"Which?","options":[{"label":"A"},{"label":"B"}]}`
	for _, mode := range []string{"normal", "plan", "auto_review", "auto_edit", "full_access", "always"} {
		m := &model{permissionMode: mode, perms: permRules{}}
		if prompted, ans := reviewDecide(t, m, "question", args); prompted || ans.decision != tools.GateAllowOnce {
			t.Errorf("%s: prompted=%v decision=%v", mode, prompted, ans.decision)
		}
		m.perms = permRules{user: permissions.RuleSet{Scope: permissions.ScopeUser, Rules: []permissions.StoredRule{{Rule: "question", Effect: permissions.Deny}}}}
		if prompted, ans := reviewDecide(t, m, "question", args); prompted || ans.decision != tools.GateReject || !strings.Contains(ans.redirect, "denied by permission rule") {
			t.Errorf("%s with deny rule: prompted=%v answer=%+v", mode, prompted, ans)
		}
	}
}
