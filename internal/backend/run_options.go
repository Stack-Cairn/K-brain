package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

var runReasoning = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

func normalizeRunOptions(in *protocol.RunOptions, cwd string, available []tools.Tool) (protocol.RunOptions, error) {
	out := protocol.RunOptions{Mode: "agent", Search: "disabled", ApprovalPolicy: "ask"}
	if in != nil {
		out = *in
		if in.Tools != nil {
			out.Tools = &protocol.ToolSelection{
				Policies: maps.Clone(in.Tools.Policies),
				Enabled:  slices.Clone(in.Tools.Enabled),
				Disabled: slices.Clone(in.Tools.Disabled),
			}
		}
		out.WorkspaceRoots = slices.Clone(in.WorkspaceRoots)
		out.MCPServerIDs = slices.Clone(in.MCPServerIDs)
	}
	if out.Mode == "" {
		out.Mode = "agent"
	}
	if out.Search == "" {
		out.Search = "disabled"
	}
	if out.ApprovalPolicy == "" {
		out.ApprovalPolicy = "ask"
	}
	if out.Mode != "agent" && out.Mode != "chat" {
		return protocol.RunOptions{}, fmt.Errorf("options.mode must be agent or chat")
	}
	if out.Search != "disabled" && out.Search != "enabled" {
		return protocol.RunOptions{}, fmt.Errorf("options.search must be disabled or enabled")
	}
	if out.ApprovalPolicy != "ask" && out.ApprovalPolicy != "auto" && out.ApprovalPolicy != "deny" {
		return protocol.RunOptions{}, fmt.Errorf("options.approval_policy is invalid")
	}
	if out.Reasoning != "" && !slices.Contains(runReasoning, out.Reasoning) {
		return protocol.RunOptions{}, fmt.Errorf("options.reasoning is invalid")
	}
	if filepath.Clean(cwd) != cwd && cwd != "" {
		cwd = filepath.Clean(cwd)
	}
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	if !filepath.IsAbs(cwd) {
		return protocol.RunOptions{}, fmt.Errorf("workspace root must be absolute")
	}
	if len(out.WorkspaceRoots) == 0 {
		out.WorkspaceRoots = []protocol.WorkspaceRoot{{Path: cwd, Access: "write"}}
	}
	for i := range out.WorkspaceRoots {
		r := &out.WorkspaceRoots[i]
		r.Path = filepath.Clean(strings.TrimSpace(r.Path))
		if !filepath.IsAbs(r.Path) || r.Path == "." {
			return protocol.RunOptions{}, fmt.Errorf("options.workspace_roots[%d].path must be absolute", i)
		}
		if r.Access != "read" && r.Access != "write" {
			return protocol.RunOptions{}, fmt.Errorf("options.workspace_roots[%d].access is invalid", i)
		}
		if info, err := os.Stat(r.Path); err != nil || !info.IsDir() {
			return protocol.RunOptions{}, fmt.Errorf("workspace root %q is unavailable", r.Path)
		}
	}
	clientTools, err := normalizeClientTools(out.ClientTools, available)
	if err != nil {
		return protocol.RunOptions{}, err
	}
	out.ClientTools = clientTools
	available = append(append([]tools.Tool(nil), available...), clientToolDefs(clientTools)...)
	if out.Tools != nil {
		if out.Tools.Policies == nil && (len(out.Tools.Enabled) > 0 || len(out.Tools.Disabled) > 0) {
			out.Tools.Policies = map[string]string{}
		}
		seen := map[string]bool{}
		check := func(name string) error {
			if name != strings.TrimSpace(name) {
				return fmt.Errorf("tool names must be exact")
			}
			if name == "" || seen[name] {
				return fmt.Errorf("options.tools contains duplicate or empty tool")
			}
			seen[name] = true
			if name != "ExitPlanMode" && !slices.ContainsFunc(available, func(t tools.Tool) bool { return t.Def.Function.Name == name }) {
				return fmt.Errorf("options.tools references unavailable tool %q", name)
			}
			return nil
		}
		for name, policy := range out.Tools.Policies {
			if err := check(name); err != nil {
				return protocol.RunOptions{}, err
			}
			if policy != "ask" && policy != "allow" && policy != "deny" {
				return protocol.RunOptions{}, fmt.Errorf("options.tools policy for %q is invalid", name)
			}
		}
		for _, name := range out.Tools.Enabled {
			if err := check(name); err != nil {
				return protocol.RunOptions{}, err
			}
			out.Tools.Policies[name] = "allow"
		}
		for _, name := range out.Tools.Disabled {
			if err := check(name); err != nil {
				return protocol.RunOptions{}, err
			}
			out.Tools.Policies[name] = "deny"
		}
		out.Tools.Enabled, out.Tools.Disabled = nil, nil
	}
	return out, nil
}

func applyRunOptions(a *agent.Agent, options protocol.RunOptions) func() {
	return applyRunOptionsWith(a, options, nil)
}

// applyRunOptionsWith also offers run-scoped extra tools (client tools) alongside the agent's
// own, under the same policy filtering.
func applyRunOptionsWith(a *agent.Agent, options protocol.RunOptions, extra []tools.Tool) func() {
	oldTools := append([]tools.Tool(nil), a.RunTools...)
	oldToolsSet, oldEffort, oldSearch, oldPlan := a.RunToolsSet, a.Effort, a.NativeWebSearch, a.PlanMode()
	a.SetPlanMode(options.PlanModeEnabled)
	available := append(a.AvailableTools(), extra...)

	if options.PlanModeEnabled {
		available = append(available, exitPlanModeTool())
	}
	if options.Tools != nil {
		filtered := make([]tools.Tool, 0, len(available))
		for _, tool := range available {
			name := tool.Def.Function.Name
			policy := options.Tools.Policies[name]
			if policy == "deny" {
				continue
			}
			if policy == "allow" || policy == "ask" {
				run := tool.Run
				tool.Run = func(ctx context.Context, args json.RawMessage) (string, error) {
					if err := tools.Authorize(ctx, name, string(args)); err != nil {
						return "", err
					}
					authorized := tools.WithGate(ctx, func(req tools.GateRequest) (tools.GateDecision, string) {
						if req.Tool == name {
							return tools.GateAllowOnce, "per-call authorization"
						}
						if err := tools.Authorize(ctx, req.Tool, req.Command); err != nil {
							return tools.GateReject, err.Error()
						}
						return tools.GateAllowOnce, ""
					})
					return run(authorized, args)
				}
			}
			filtered = append(filtered, tool)
		}
		available = filtered
	}
	if options.Mode == "chat" {
		available = nil
	}
	a.RunTools = available
	a.RunToolsSet = true
	a.NativeWebSearch = options.Search == "enabled"
	if options.Reasoning == "off" {
		a.Effort = ""
	} else if options.Reasoning != "" {
		a.Effort = options.Reasoning
	}
	return func() {
		a.RunTools = oldTools
		a.RunToolsSet = oldToolsSet
		a.Effort = oldEffort
		a.NativeWebSearch = oldSearch
		a.SetPlanMode(oldPlan)
	}
}

func exitPlanModeTool() tools.Tool {
	return tools.Tool{
		Def: ai.NewTool("ExitPlanMode", "Submit the complete markdown plan for user approval. This ends the planning turn; implementation requires a subsequent approved turn.", `{"type":"object","additionalProperties":false,"properties":{"plan":{"type":"string"}},"required":["plan"]}`),
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			var args struct {
				Plan string `json:"plan"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", err
			}
			if strings.TrimSpace(args.Plan) == "" {
				return "", fmt.Errorf("plan is required")
			}
			if err := tools.Authorize(ctx, "ExitPlanMode", args.Plan); err != nil {
				return "", err
			}
			return "Plan submitted; this turn ends here. The user will reply with approval or feedback.\n\n" + args.Plan, nil
		},
	}
}

func runToolGate(options protocol.RunOptions, req tools.GateRequest, ask func(tools.GateRequest) (tools.GateDecision, string)) (tools.GateDecision, string) {
	if options.PlanModeEnabled && req.Tool != "Read" && req.Tool != "Image" && req.Tool != "List" && req.Tool != "Glob" && req.Tool != "Grep" && req.Tool != "ExitPlanMode" && req.Tool != "read" && req.Tool != "question" && req.Tool != "AskUserQuestion" && req.Tool != "todowrite" {
		return tools.GateReject, "plan mode blocks this tool until ExitPlanMode is approved"
	}
	if options.ApprovalPolicy == "deny" {
		return tools.GateReject, "approval policy denies this action"
	}
	if options.Tools != nil {
		switch options.Tools.Policies[req.Tool] {
		case "deny":
			return tools.GateReject, "per-tool policy denies this action"
		case "allow":
			return tools.GateAllowOnce, "per-tool policy allows this action"
		case "ask":
			return ask(req)
		}
	}
	if req.Tool == "ExitPlanMode" || options.ApprovalPolicy == "auto" {
		return tools.GateAllowOnce, "per-run approval policy"
	}
	return ask(req)
}
