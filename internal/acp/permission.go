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
		planAsks := tools.AsksInPlanMode(req.Tool)
		switch {
		case mode == ModeAuto:
			return tools.GateAllowOnce, ""
		case mode == ModePlan && !planAsks:
			return tools.GateReject, "Plan mode blocks this action; switch modes before implementation"
		}
		decision, reason := b.requestPermission(req.Context, s, req)
		s.turnMu.Lock()
		blocked := s.closed || (s.mode == ModePlan && !planAsks)
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
	preq := permissions.Request{Tool: req.Tool, Command: req.Command, Shell: req.Shell, Poll: req.Poll, OtherDir: req.OtherDir}
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

	call := acp.ToolCallUpdate{
		ToolCallId: acp.ToolCallId("perm-" + req.Tool),
		Title:      new(name),
		Kind:       new(toolKind(req.Tool)),
	}
	if p := req.Preview; p != nil {
		call.Locations = []acp.ToolCallLocation{{Path: p.Path}}
		if text := previewText(p); text != "" {
			call.Content = []acp.ToolCallContent{acp.ToolContent(acp.TextBlock(text))}
		}
	}
	resp, err := b.conn.RequestPermission(ctx, acp.RequestPermissionRequest{
		SessionId: s.id,
		ToolCall:  call,
		Options:   options,
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

// previewText renders a pending file change for the permission prompt: the
// preview diff as a fenced block, or a note when it is too large to show.
func previewText(p *tools.EditPreview) string {
	switch {
	case p.Diff != "":
		text := "```diff\n" + p.Diff + "\n```"
		if p.Truncated {
			text += "\n(preview truncated)"
		}
		return text
	case p.Truncated:
		return "(change too large to preview)"
	}
	return ""
}
