package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/permissions"
	"github.com/Stack-Cairn/K-brain/internal/routing"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// repeatedFlag collects every value of a flag that may be given more than once.
type repeatedFlag []string

func (f *repeatedFlag) String() string { return strings.Join(*f, ", ") }

func (f *repeatedFlag) Set(v string) error {
	*f = append(*f, v)
	return nil
}

// splitToolList parses a comma-separated -allowed-tools / -disallowed-tools value.
func splitToolList(v string) []string {
	var out []string
	for name := range strings.SplitSeq(v, ",") {
		if name = strings.TrimSpace(name); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// restrictRunTools narrows the tools of this run to allowed (all when empty) minus
// disallowed. It uses the agent's run tool set, the same mechanism a backend run's
// tool selection uses, so a withheld tool is neither offered to the model nor run.
func restrictRunTools(ag *agent.Agent, allowed, disallowed []string) error {
	if len(allowed) == 0 && len(disallowed) == 0 {
		return nil
	}
	available := ag.AvailableTools()
	known := make([]string, 0, len(available))
	for _, tool := range available {
		known = append(known, tool.Def.Function.Name)
	}
	slices.Sort(known)
	for _, list := range []struct {
		flag  string
		names []string
	}{{"-allowed-tools", allowed}, {"-disallowed-tools", disallowed}} {
		for _, name := range list.names {
			if !slices.Contains(known, name) {
				return fmt.Errorf("%s: unknown tool %q; valid tools: %s", list.flag, name, strings.Join(known, ", "))
			}
		}
	}
	kept := make([]tools.Tool, 0, len(available))
	for _, tool := range available {
		name := tool.Def.Function.Name
		if (len(allowed) > 0 && !slices.Contains(allowed, name)) || slices.Contains(disallowed, name) {
			continue
		}
		kept = append(kept, tool)
	}
	ag.RunTools, ag.RunToolsSet = kept, true
	return nil
}

// runRuleSet parses the -allow and -deny rules into a session-scope rule set that
// lives only for this run; nothing is written to the rules files.
func runRuleSet(allow, deny []string) (permissions.RuleSet, error) {
	set := permissions.RuleSet{Scope: permissions.ScopeSession}
	for _, group := range []struct {
		flag   string
		effect permissions.Effect
		rules  []string
	}{{"-allow", permissions.Allow, allow}, {"-deny", permissions.Deny, deny}} {
		for _, text := range group.rules {
			rule, err := permissions.ParseRule(text)
			if err != nil {
				return set, fmt.Errorf("%s: %w", group.flag, err)
			}
			set.Rules = append(set.Rules, permissions.StoredRule{Rule: rule.String(), Effect: group.effect})
		}
	}
	return set, nil
}

// runRulesGate decides a gated call from the run's -allow/-deny rules first: a deny
// rule always rejects, even in full_access or always mode; an allow rule lets the
// call through even in normal mode. Anything no rule decides goes to base, the
// permission mode's gate, or is allowed when no mode was given (the usual headless
// behavior).
func runRulesGate(set permissions.RuleSet, root string, base func(tools.GateRequest) (tools.GateDecision, string)) func(tools.GateRequest) (tools.GateDecision, string) {
	return func(req tools.GateRequest) (tools.GateDecision, string) {
		decision := permissions.Evaluate(permissions.Request{Tool: req.Tool, Command: req.Command, Shell: req.Shell, Poll: req.Poll, OtherDir: req.OtherDir}, permissions.Options{ProjectRoot: root}, set)
		switch decision.Effect {
		case permissions.Deny:
			return tools.GateReject, "denied by the run's -deny rule " + decision.Rule
		case permissions.Ask:
			return tools.GateReject, "this command cannot be checked against the run's -deny rules"
		case permissions.Allow:
			return tools.GateAllowOnce, "allowed by the run's -allow rule " + decision.Rule
		}
		if base == nil {
			return tools.GateAllowOnce, ""
		}
		return base(req)
	}
}

// absImagePaths resolves -image paths against the directory kn run was started in,
// before -cwd moves the run elsewhere.
func absImagePaths(paths []string) ([]string, error) {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return nil, fmt.Errorf("-image %s: %w", p, err)
		}
		out = append(out, abs)
	}
	return out, nil
}

// loadRunImages reads -image files through the loader the TUI uses for attached
// images. The model must accept image input.
func loadRunImages(paths []string, vision bool, model string) ([]ai.ContentPart, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	if !vision {
		return nil, fmt.Errorf("-image: model %s does not accept image input", model)
	}
	parts := make([]ai.ContentPart, 0, len(paths))
	for _, p := range paths {
		part, err := ai.LoadImageFile(p)
		if err != nil {
			return nil, fmt.Errorf("-image: %w", err)
		}
		parts = append(parts, part)
	}
	return parts, nil
}

// runEffort validates -effort against the levels the model supports.
func runEffort(catalogs map[string]config.Catalog, provider, model, level string) (string, error) {
	levels := routing.EffortsFor(catalogs, provider, model)
	if lv, ok := routing.ParseEffort(levels, strings.ToLower(strings.TrimSpace(level))); ok {
		return lv, nil
	}
	names := make([]string, len(levels))
	for i, e := range levels {
		names[i] = e
		if e == "" {
			names[i] = "off"
		}
	}
	return "", fmt.Errorf("-effort: unknown level %q; %s supports: %s", level, model, strings.Join(names, ", "))
}

// enterRunDir switches the process to dir for -cwd and returns how to switch back.
func enterRunDir(dir string) (func(), error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("-cwd: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("-cwd: %s is not a directory", dir)
	}
	prev, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("-cwd: %w", err)
	}
	if err := os.Chdir(dir); err != nil {
		return nil, fmt.Errorf("-cwd: %w", err)
	}
	return func() { _ = os.Chdir(prev) }, nil
}
