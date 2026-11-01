package backend

import (
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/permissions"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func TestPermissionModesGatePrecedence(t *testing.T) {
	for _, tc := range []struct {
		mode, tool, policy, approval string
		allowed, prompted            bool
	}{
		{"normal", "Write", "", "auto", false, true},
		{"auto_edit", "Write", "", "ask", true, false},
		{"auto_edit", "Edit", "", "ask", true, false},
		{"auto_edit", "Bash", "", "auto", false, true},
		{"auto_edit", "Delete", "", "auto", false, true},
		{"full_access", "Bash", "", "ask", true, false},
		{"full_access", "Bash", "ask", "ask", false, true},
		{"always", "Bash", "ask", "ask", true, false},
		{"always", "Bash", "deny", "auto", false, false},
		{"always", "Write", "allow", "deny", false, false},
		{"plan", "Write", "allow", "auto", false, false},
		{"auto_review", "Bash", "", "auto", false, true},
	} {
		t.Run(tc.mode+"/"+tc.tool+"/"+tc.policy+"/"+tc.approval, func(t *testing.T) {
			options, err := normalizeRunOptions(&protocol.RunOptions{PermissionMode: tc.mode, ApprovalPolicy: tc.approval}, t.TempDir(), nil)
			if err != nil {
				t.Fatal(err)
			}
			options.Tools = &protocol.ToolSelection{Policies: map[string]string{tc.tool: tc.policy}}
			prompted := false
			decision, _ := runToolGate(options, tools.GateRequest{Tool: tc.tool}, permissions.Decision{}, func(tools.GateRequest) (tools.GateDecision, string) { prompted = true; return tools.GateReject, "test" })
			if (decision == tools.GateAllowOnce) != tc.allowed || prompted != tc.prompted {
				t.Fatalf("decision=%v prompted=%v", decision, prompted)
			}
		})
	}
	if _, err := normalizeRunOptions(&protocol.RunOptions{PermissionMode: "typo"}, t.TempDir(), nil); err == nil {
		t.Fatal("unknown mode accepted")
	}
}
