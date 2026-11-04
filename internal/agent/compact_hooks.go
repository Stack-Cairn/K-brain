package agent

import (
	"context"

	"github.com/Stack-Cairn/K-brain/internal/hooks"
)

const (
	compactAuto   = "auto"
	compactManual = "manual"
)

// compactHooked runs compact between the PreCompact and PostCompact hooks. Both
// hooks only observe: a failing hook never blocks or undoes a compaction, because
// automatic compaction is what keeps the next request inside the context window.
// PostCompact runs only after a compaction that changed the context.
func (a *Agent) compactHooked(ctx context.Context, trigger string) (string, int, CompactInfo, error) {
	if a.Hooks == nil && a.PluginHook == nil {
		return a.compact(ctx)
	}
	before := a.MessagesSnapshot()
	event := hooks.Event{Trigger: trigger, MessagesBefore: len(before), TokensBefore: EstimateTokens(before)}
	if len(before) > 3 { // compact refuses shorter histories; nothing is about to change

		pre := event
		pre.Name = hooks.PreCompact
		_ = a.runHook(ctx, pre)
	}
	summary, cutoff, info, err := a.compact(ctx)
	if err != nil {
		return summary, cutoff, info, err
	}
	after := a.MessagesSnapshot()
	event.Name, event.MessagesAfter, event.TokensAfter, event.Summary = hooks.PostCompact, len(after), EstimateTokens(after), summary
	_ = a.runHook(context.WithoutCancel(ctx), event)
	return summary, cutoff, info, nil
}
