package acp

import (
	"context"
	"fmt"
	"strings"

	acp "github.com/coder/acp-go-sdk"

	"github.com/Stack-Cairn/K-brain/internal/permissions"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

const (
	optAllowOnce   = "allow-once"
	optAllowAlways = "allow-always"
	optReject      = "reject"
)

func (b *Bridge) permissionContext(ctx context.Context, s *acpSession) context.Context {
	return tools.WithGate(ctx, func(req tools.GateRequest) (tools.GateDecision, string) {
		s.turnMu.Lock()
		mode := s.mode
		s.turnMu.Unlock()
		switch mode {
		case ModeAuto:
			return tools.GateAllowOnce, ""
		case ModePlan:
			return tools.GateReject, "Plan mode blocks this action; switch modes before implementation"
		}
		decision, reason := b.requestPermission(req.Context, s, req)
		s.turnMu.Lock()
		blocked := s.closed || s.mode == ModePlan
		s.turnMu.Unlock()
		if blocked {
			return tools.GateReject, "session closed or switched to Plan mode while awaiting permission"
		}
		if err := req.Context.Err(); err != nil {
			return tools.GateReject, err.Error()
		}
		return decision, reason
	})
}

func (b *Bridge) requestPermission(ctx context.Context, s *acpSession, req tools.GateRequest) (tools.GateDecision, string) {
	if b.conn == nil {
		return tools.GateAllowOnce, ""
	}

	// Rules are session-scoped and matched per command segment, so remembering
	// `make build` does not cover `make build && rm -rf x`.
	preq := permissions.Request{Tool: req.Tool, Command: req.Command, Shell: req.Shell}
	opts := permissions.Options{ProjectRoot: s.ag.WorkingDir}
	s.turnMu.Lock()
	covered := permissions.Evaluate(preq, opts, s.allowed).Effect == permissions.Allow
	s.turnMu.Unlock()
	if covered {
		return tools.GateAllowOnce, ""
	}
	suggested := permissions.Suggest(preq, opts)

	name := req.Tool
	if req.Command != "" {
		name = fmt.Sprintf("%s %q", req.Tool, req.Command)
	}
	options := []acp.PermissionOption{
		{OptionId: optAllowOnce, Name: "Allow once", Kind: acp.PermissionOptionKindAllowOnce},
		{OptionId: optReject, Name: "Reject", Kind: acp.PermissionOptionKindRejectOnce},
	}
	if len(suggested) > 0 {
		options = append(options, acp.PermissionOption{
			OptionId: optAllowAlways,
			Name:     "Always allow " + strings.Join(suggested, ", "),
			Kind:     acp.PermissionOptionKindAllowAlways,
		})
	}

	resp, err := b.conn.RequestPermission(ctx, acp.RequestPermissionRequest{
		SessionId: s.id,
		ToolCall: acp.ToolCallUpdate{
			ToolCallId: acp.ToolCallId("perm-" + req.Tool),
			Title:      new(name),
			Kind:       new(toolKind(req.Tool)),
		},
		Options: options,
	})
	if err != nil {
		if ctx.Err() != nil {
			return tools.GateReject, "the user cancelled the permission prompt"
		}
		return tools.GateReject, "permission request failed: " + err.Error()
	}
	switch {
	case resp.Outcome.Selected != nil:
		switch string(resp.Outcome.Selected.OptionId) {
		case optAllowOnce:
			return tools.GateAllowOnce, ""
		case optAllowAlways:
			if len(suggested) == 0 {
				return tools.GateAllowOnce, ""
			}
			s.turnMu.Lock()
			for _, rule := range suggested {
				s.allowed.Rules = append(s.allowed.Rules, permissions.StoredRule{Rule: rule, Effect: permissions.Allow})
			}
			s.turnMu.Unlock()
			return tools.GateAllowAlways, ""
		default:
			return tools.GateReject, "the user rejected this action"
		}
	default:
		return tools.GateReject, "the user cancelled the permission prompt"
	}
}
