package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/permissions"
	"github.com/Stack-Cairn/K-brain/internal/tools/bashrun"
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
	// Shell is the resolved shell that will run Command, for tools that run one;
	// empty when unknown, which permission rules parse strictly.
	Shell       string
	Rule        string
	Description string
}

var Gate func(GateRequest) (GateDecision, string)

type gateContextKey struct{}

type contextGate struct {
	check func(GateRequest) (GateDecision, string)
}

func WithGate(ctx context.Context, gate func(GateRequest) (GateDecision, string)) context.Context {
	return context.WithValue(ctx, gateContextKey{}, contextGate{check: gate})
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
	return strings.Join(permissions.CommandPrefix(tokens), " ")
}

// bashShell is the shell the TUI's bash tool runs a command with: its shell
// argument, else the platform default (the tool replaces any shell in ctx).
func bashShell(ctx context.Context, shell string) string {
	return bashrun.ResolveShell(bashrun.WithShell(ctx, shell), "")
}

// liveagentShell is the shell LiveAgent's Bash runs a command with: its shell
// argument, else the shell in ctx, else the platform default.
func liveagentShell(ctx context.Context, shell string) string {
	return bashrun.ResolveShell(ctx, shell)
}

// GateRequestFor builds the request a tool's own gate check makes for args, so that a
// caller gating the call before it runs (AI review) shows the same command, shell and
// path, and permission rules match it the same way. Tools without a known target
// keep the raw arguments as the command.
func GateRequestFor(ctx context.Context, tool string, args json.RawMessage) GateRequest {
	req := GateRequest{Context: ctx, Tool: tool, Command: string(args)}
	var a struct {
		Command string `json:"command"`
		Shell   string `json:"shell"`
		Path    string `json:"path"`
	}
	if json.Unmarshal(args, &a) == nil {
		switch tool {
		case "bash":
			if a.Command != "" {
				req.Command, req.Shell = a.Command, bashShell(ctx, a.Shell)
			}
		case "Bash":
			if a.Command != "" {
				req.Command, req.Shell = a.Command, liveagentShell(ctx, a.Shell)
			}
		case "write", "edit":
			if a.Path != "" {
				req.Command = ResolvePath(ctx, a.Path)
			}
		case "Write", "Edit", "Delete", "Read":
			if a.Path != "" {
				req.Command = a.Path
				access := "write"
				if tool == "Read" {
					access = "read"
				}
				if resolved, err := liveagentPath(ctx, a.Path, access, tool, tool == "Delete"); err == nil {
					req.Command = resolved
				}
			}
		}
	}
	req.Rule = CommandRule(req.Command)
	return req
}

func checkGate(ctx context.Context, tool, command string) string {
	return checkShellGate(ctx, tool, command, "")
}

// checkShellGate is checkGate for a command that shell will run.
func checkShellGate(ctx context.Context, tool, command, shell string) string {
	var gate func(GateRequest) (GateDecision, string)
	if scoped, ok := ctx.Value(gateContextKey{}).(contextGate); ok {
		gate = scoped.check
	} else {
		gate = Gate
	}
	if gate == nil {
		return ""
	}
	decision, redirect := gate(GateRequest{Context: ctx, Tool: tool, Command: command, Shell: shell, Rule: CommandRule(command)})
	if decision == GateReject {
		if redirect == "" {
			redirect = "the user rejected this action"
		}
		return "Permission denied: " + redirect
	}
	return ""
}
