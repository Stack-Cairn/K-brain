package tools

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"runtime"
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

// AuthorizePoll gates a call that only reads the output of a process or terminal this
// session started; id names it. The caller still checks that the session owns it.
func AuthorizePoll(ctx context.Context, tool, id string) error {
	if deny := checkGateRequest(ctx, GateRequest{Context: ctx, Tool: tool, Command: id, Rule: CommandRule(id), Poll: true}); deny != "" {
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
	// Poll marks a call that only reads the output of a process or terminal this
	// session started; the tool checks ownership after the gate.
	Poll bool
	// OtherDir marks a command that runs outside the session's working directory
	// because of a cwd argument.
	OtherDir bool
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
		Cwd     string `json:"cwd"`
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
			req.OtherDir = cwdOutsideWorkingDir(ctx, a.Cwd, "Bash")
		case "write", "edit":
			if a.Path != "" {
				req.Command = ResolvePath(ctx, a.Path)
			}
		case "ProcessWait", "ReadTerminal", "ManagedProcess", "ProcessStop":
			pollRequest(&req, args)
			if tool == "ManagedProcess" && !req.Poll {
				req.OtherDir = cwdOutsideWorkingDir(ctx, a.Cwd, "ManagedProcess")
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

// pollRequest fills the request the process and terminal tools gate with: the id for
// a poll (wait, status, read_log, ReadTerminal) or a stop, the command line for a start.
func pollRequest(req *GateRequest, args json.RawMessage) {
	var a struct {
		Action    string `json:"action"`
		Command   string `json:"command"`
		ProcessID string `json:"process_id"`
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal(args, &a) != nil {
		return
	}
	id := strings.TrimSpace(a.ProcessID)
	if id == "" {
		id = strings.TrimSpace(a.SessionID)
	}
	switch req.Tool {
	case "ProcessWait", "ReadTerminal":
		req.Command, req.Poll = id, true
	case "ProcessStop":
		req.Command = id
	case "ManagedProcess":
		switch strings.ToLower(strings.TrimSpace(a.Action)) {
		case "status":
			req.Command, req.Poll = id, true
			if id == "" {
				req.Command = "status"
			}
		case "read_log", "wait":
			req.Command, req.Poll = id, true
		case "start":
			req.Command = a.Command
		case "stop":
			req.Command = id
		}
	}
}

// cwdOutsideWorkingDir reports a cwd argument naming a directory other than the
// session's working directory. An empty or unresolvable cwd is not one: the tool
// refuses a cwd it cannot resolve.
func cwdOutsideWorkingDir(ctx context.Context, cwd, tool string) bool {
	if strings.TrimSpace(cwd) == "" {
		return false
	}
	dir, err := liveagentPath(ctx, cwd, "write", tool, true)
	if err != nil {
		return false
	}
	return !sameDirectory(dir, WorkingDir(ctx))
}

func sameDirectory(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// checkShellGate is checkGate for a command that shell will run.
func checkShellGate(ctx context.Context, tool, command, shell string) string {
	return checkGateRequest(ctx, GateRequest{Context: ctx, Tool: tool, Command: command, Shell: shell, Rule: CommandRule(command)})
}

func checkGateRequest(ctx context.Context, req GateRequest) string {
	var gate func(GateRequest) (GateDecision, string)
	if scoped, ok := ctx.Value(gateContextKey{}).(contextGate); ok {
		gate = scoped.check
	} else {
		gate = Gate
	}
	if gate == nil {
		return ""
	}
	decision, redirect := gate(req)
	if decision == GateReject {
		if redirect == "" {
			redirect = "the user rejected this action"
		}
		return "Permission denied: " + redirect
	}
	return ""
}
