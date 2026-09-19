package tools

import (
	"context"
	"errors"
	"strings"
)

func Authorize(ctx context.Context, tool, command string) error {
	if deny := checkGate(ctx, tool, command); deny != "" {
		return errors.New(deny)
	}
	return nil
}

type GateDecision int

const (
	GateAllowOnce GateDecision = iota
	GateAllowAlways
	GateReject
)

type GateRequest struct {
	Context context.Context
	Tool    string
	Command string
	Rule    string
}

var Gate func(GateRequest) (GateDecision, string)

type gateContextKey struct{}

type contextGate struct {
	check func(GateRequest) (GateDecision, string)
}

func WithGate(ctx context.Context, gate func(GateRequest) (GateDecision, string)) context.Context {
	return context.WithValue(ctx, gateContextKey{}, contextGate{check: gate})
}

var arity = map[string]int{

	"ls": 1, "cat": 1, "pwd": 1, "grep": 1, "find": 1, "echo": 1,
	"rm": 1, "mv": 1, "cp": 1, "mkdir": 1, "touch": 1, "which": 1,

	"git": 2, "npm": 2, "pnpm": 2, "yarn": 2, "go": 2, "cargo": 2,
	"docker": 2, "kubectl": 2, "brew": 2, "apt": 2, "pip": 2,

	"npm run": 3, "pnpm run": 3, "go tool": 3, "docker compose": 3, "git submodule": 3,
}

func CommandRule(command string) string {
	cmd := strings.TrimSpace(command)

	for i, r := range cmd {
		if r == '&' || r == '|' || r == ';' || r == '>' || r == '<' {
			cmd = strings.TrimSpace(cmd[:i])
			break
		}
	}
	tokens := strings.Fields(cmd)
	for i := 0; i < len(tokens); i++ {
		if strings.Contains(tokens[i], "=") && !strings.HasPrefix(tokens[i], "-") && i == 0 {

			tokens = tokens[1:]
			i = -1
		}
	}
	if len(tokens) == 0 {
		return ""
	}

	for n := len(tokens); n > 0; n-- {
		prefix := strings.Join(tokens[:n], " ")
		if a, ok := arity[prefix]; ok {
			return strings.Join(tokens[:min(a, len(tokens))], " ")
		}
	}
	return tokens[0]
}

func checkGate(ctx context.Context, tool, command string) string {
	var gate func(GateRequest) (GateDecision, string)
	if scoped, ok := ctx.Value(gateContextKey{}).(contextGate); ok {
		gate = scoped.check
	} else {
		gate = Gate
	}
	if gate == nil {
		return ""
	}
	decision, redirect := gate(GateRequest{Context: ctx, Tool: tool, Command: command, Rule: CommandRule(command)})
	if decision == GateReject {
		if redirect == "" {
			redirect = "the user rejected this action"
		}
		return "Permission denied: " + redirect
	}
	return ""
}
