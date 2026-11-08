package tui

import (
	"context"

	"github.com/Stack-Cairn/K-brain/internal/approval"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func (m *model) withAIApproval(ctx context.Context, intent string) context.Context {
	if m.permissionMode == "always" || m.cfg == nil {
		return ctx
	}
	cfg := m.cfg.Snapshot()
	if m.permissionMode == "auto_review" {
		cfg.AIApproval.Mode = "auto"
	}
	if !cfg.AIApproval.Enabled() {
		return ctx
	}
	post := m.poster()
	run := approval.NewRun(cfg.AIApproval.Mode, intent, approval.New(ctx, cfg), func(phase, tool string, report approval.Report, automatic bool) {
		if post != nil && (!report.Safe() || automatic) {
			post(noticeMsg("AI " + phase + " " + tool + ": " + report.Description()))
		}
	})
	return tools.WithReview(ctx, run.Hooks(func(req tools.GateRequest) (tools.GateDecision, string) {
		if tools.Gate == nil {
			return tools.GateReject, "permission interface unavailable"
		}
		return tools.Gate(req)
	}))
}
