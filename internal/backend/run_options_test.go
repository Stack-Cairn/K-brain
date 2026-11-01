package backend

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/permissions"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func TestNormalizeRunOptionsDefaultsAndRejectsInvalidCapabilities(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	available := []tools.Tool{{Def: ai.NewTool("Read", "read", `{"type":"object"}`)}, {Def: ai.NewTool("Write", "write", `{"type":"object"}`)}}
	got, err := normalizeRunOptions(nil, root, available)
	if err != nil || got.Mode != "agent" || got.ApprovalPolicy != "ask" || len(got.WorkspaceRoots) != 1 {
		t.Fatalf("defaults = %+v, err=%v", got, err)
	}
	granted, err := normalizeRunOptions(&protocol.RunOptions{Reasoning: "minimal", WorkspaceRoots: []protocol.WorkspaceRoot{{Path: root, Access: "write"}, {Path: other, Access: "read"}}}, root, available)
	if err != nil || len(granted.WorkspaceRoots) != 2 || granted.Reasoning != "minimal" {
		t.Fatalf("explicit roots/reasoning = %+v, err=%v", granted, err)
	}
	for _, reasoning := range []string{"minimal", "max"} {
		got, err := normalizeRunOptions(&protocol.RunOptions{Reasoning: reasoning}, root, available)
		if err != nil || got.Reasoning != reasoning {
			t.Fatalf("reasoning %q = %+v, err=%v", reasoning, got, err)
		}
	}
	if _, err := normalizeRunOptions(&protocol.RunOptions{Mode: "chat", Reasoning: "invalid"}, root, available); err == nil {
		t.Fatal("invalid reasoning was accepted")
	}
	if _, err := normalizeRunOptions(&protocol.RunOptions{Tools: &protocol.ToolSelection{Policies: map[string]string{"write": "bogus"}}}, root, available); err == nil {
		t.Fatal("invalid tool policy was accepted")
	}
	if _, err := normalizeRunOptions(&protocol.RunOptions{Tools: &protocol.ToolSelection{Enabled: []string{"bash"}}}, root, available); err == nil {
		t.Fatal("unavailable tool was accepted")
	}
}

func TestApplyRunOptionsFiltersToolsAndRestoresAgent(t *testing.T) {
	a := agent.New(nil, "model", 100, "system")
	a.Tools = []tools.Tool{{Def: ai.NewTool("Read", "read", `{"type":"object"}`)}, {Def: ai.NewTool("Write", "write", `{"type":"object"}`)}, {Def: ai.NewTool("Bash", "bash", `{"type":"object"}`)}}
	a.Effort = "low"
	restore := applyRunOptions(a, protocol.RunOptions{Mode: "agent", Reasoning: "max", Search: "enabled", Tools: &protocol.ToolSelection{Policies: map[string]string{"Write": "deny"}}})
	if len(a.AllTools()) != 2 || a.Effort != "max" || !a.NativeWebSearch || a.PlanMode() {
		t.Fatalf("applied options: tools=%v effort=%q search=%v plan=%v", a.AllTools(), a.Effort, a.NativeWebSearch, a.PlanMode())
	}
	for _, tool := range a.AllTools() {
		if tool.Def.Function.Name == "Write" {
			t.Fatal("denied tool remained available")
		}
	}
	restore()
	if a.Effort != "low" || a.NativeWebSearch || a.RunToolsSet || a.PlanMode() {
		t.Fatalf("restore failed: effort=%q search=%v runToolsSet=%v plan=%v", a.Effort, a.NativeWebSearch, a.RunToolsSet, a.PlanMode())
	}
}

func TestRunToolGateEnforcesPerToolPoliciesAndPlanMode(t *testing.T) {
	if decision, _ := runToolGate(protocol.RunOptions{ApprovalPolicy: "deny", Tools: &protocol.ToolSelection{Policies: map[string]string{"Write": "allow"}}}, tools.GateRequest{Tool: "Write"}, permissions.Decision{}, nil); decision != tools.GateReject {
		t.Fatal("global deny was bypassed")
	}
	askCalls := 0
	ask := func(tools.GateRequest) (tools.GateDecision, string) {
		askCalls++
		return tools.GateAllowOnce, "asked"
	}
	if decision, _ := runToolGate(protocol.RunOptions{ApprovalPolicy: "ask", Tools: &protocol.ToolSelection{Policies: map[string]string{"Write": "allow", "Bash": "ask", "Read": "deny"}}}, tools.GateRequest{Tool: "Write"}, permissions.Decision{}, ask); decision != tools.GateAllowOnce {
		t.Fatal("per-tool allow did not override default ask")
	}
	if decision, _ := runToolGate(protocol.RunOptions{ApprovalPolicy: "auto", Tools: &protocol.ToolSelection{Policies: map[string]string{"Write": "deny"}}}, tools.GateRequest{Tool: "Write"}, permissions.Decision{}, ask); decision != tools.GateReject {
		t.Fatal("per-tool deny did not override global auto")
	}
	if decision, _ := runToolGate(protocol.RunOptions{ApprovalPolicy: "auto", Tools: &protocol.ToolSelection{Policies: map[string]string{"Bash": "ask"}}}, tools.GateRequest{Tool: "Bash"}, permissions.Decision{}, ask); decision != tools.GateAllowOnce || askCalls != 1 {
		t.Fatal("per-tool ask was not enforced")
	}
	if decision, _ := runToolGate(protocol.RunOptions{ApprovalPolicy: "auto", PlanModeEnabled: true}, tools.GateRequest{Tool: "Write"}, permissions.Decision{}, ask); decision != tools.GateReject {
		t.Fatal("plan mode allowed a write")
	}
	if decision, _ := runToolGate(protocol.RunOptions{ApprovalPolicy: "auto", PlanModeEnabled: true}, tools.GateRequest{Tool: "Read"}, permissions.Decision{}, ask); decision != tools.GateAllowOnce {
		t.Fatal("plan mode blocked a read")
	}
}

func TestApplyRunOptionsPlanModeRestrictsTools(t *testing.T) {
	a := agent.New(nil, "model", 100, "system")
	a.Tools = append(tools.LiveAgentCatalog(), tools.Tool{Def: ai.NewTool("custom", "custom", `{"type":"object"}`)})
	restore := applyRunOptions(a, protocol.RunOptions{Mode: "agent", PlanModeEnabled: true})
	defer restore()
	if !a.PlanMode() {
		t.Fatal("plan mode was not enabled")
	}
	for _, tool := range a.AllTools() {
		if tool.Def.Function.Name == "Write" || tool.Def.Function.Name == "Bash" || tool.Def.Function.Name == "custom" {
			t.Fatalf("mutating tool available in plan mode: %s", tool.Def.Function.Name)
		}
	}
}

func TestRunOptionsExecuteGrantedRootsAndToolPolicies(t *testing.T) {
	primary, external, ungranted := t.TempDir(), t.TempDir(), t.TempDir()
	catalog := tools.LiveAgentCatalog()
	for _, dir := range []string{primary, external, ungranted} {
		if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("original"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, access := range []string{"read", "write"} {
		for _, policy := range []string{"allow", "ask", "deny"} {
			t.Run(access+"/"+policy, func(t *testing.T) {
				options, err := normalizeRunOptions(&protocol.RunOptions{ApprovalPolicy: "auto", WorkspaceRoots: []protocol.WorkspaceRoot{{Path: primary, Access: "write"}, {Path: external, Access: access}}, Tools: &protocol.ToolSelection{Policies: map[string]string{"Read": policy, "Write": policy}}}, primary, catalog)
				if err != nil {
					t.Fatal(err)
				}
				asks := 0
				ctx := tools.WithWorkingDir(context.Background(), primary)
				ctx = tools.WithWorkspaceRoots(ctx, []tools.WorkspaceRoot{{Path: primary, Access: "write"}, {Path: external, Access: access}})
				ctx = tools.WithGate(ctx, func(req tools.GateRequest) (tools.GateDecision, string) {
					return runToolGate(options, req, permissions.Decision{}, func(tools.GateRequest) (tools.GateDecision, string) { asks++; return tools.GateReject, "test refusal" })
				})
				args, _ := json.Marshal(map[string]string{"path": filepath.Join(external, "a.txt")})
				read := tools.ExecuteResult(ctx, catalog, "Read", args, false)
				if read.Failed != (policy != "allow") {
					t.Fatalf("read=%+v", read)
				}
				if policy == "ask" && asks != 1 {
					t.Fatalf("ask count=%d", asks)
				}
				args, _ = json.Marshal(map[string]string{"path": filepath.Join(external, "created.txt"), "content": "changed"})
				write := tools.ExecuteResult(ctx, catalog, "Write", args, false)
				if write.Failed != (policy != "allow" || access != "write") {
					t.Fatalf("write=%+v", write)
				}
				args, _ = json.Marshal(map[string]string{"path": filepath.Join(ungranted, "a.txt")})
				if got := tools.ExecuteResult(ctx, catalog, "Read", args, false); !got.Failed {
					t.Fatal("ungranted root was readable")
				}
			})
		}
	}
}

func TestRunOptionsAllowRetainsUnspecifiedToolsAndPlanExecution(t *testing.T) {
	a := agent.New(nil, "model", 100, "system")
	a.Tools = tools.LiveAgentCatalog()
	options, err := normalizeRunOptions(&protocol.RunOptions{Tools: &protocol.ToolSelection{Enabled: []string{"Read"}}}, t.TempDir(), a.Tools)
	if err != nil {
		t.Fatal(err)
	}
	restore := applyRunOptions(a, options)
	if len(a.AllTools()) != len(a.Tools) {
		t.Fatal("allow entry restricted unrelated tools")
	}
	restore()
	restore = applyRunOptions(a, protocol.RunOptions{Mode: "agent", PlanModeEnabled: true, ApprovalPolicy: "auto"})
	defer restore()
	ctx := tools.WithWorkingDir(context.Background(), t.TempDir())
	if got := tools.ExecuteResult(ctx, a.AllTools(), "Write", json.RawMessage(`{"path":"blocked","content":"bad"}`), false); !got.Failed {
		t.Fatal("plan Write executed")
	}
	result := tools.ExecuteResult(ctx, a.AllTools(), "ExitPlanMode", json.RawMessage(`{"plan":"Review then implement"}`), false)
	if result.Failed || !strings.Contains(result.Text, "Review then implement") || !a.PlanMode() {
		t.Fatalf("plan submission=%+v plan=%v", result, a.PlanMode())
	}
	if got := tools.ExecuteResult(ctx, a.AllTools(), "Write", json.RawMessage(`{"path":"blocked","content":"bad"}`), false); !got.Failed {
		t.Fatal("plan submission enabled Write")
	}
}
