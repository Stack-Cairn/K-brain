package agent

import "context"

// ManualCompactCommit keeps the turn lock until the caller commits the new context.
// A cancelled or failed commit restores the previous context and usage ledger.
func (a *Agent) ManualCompactCommit(ctx context.Context, ev Events, commit func() error) (err error) {
	if !a.turnMu.TryLock() {
		return ErrBusy
	}
	defer a.turnMu.Unlock()
	before := a.MessagesSnapshot()
	usage := a.UsageSummary()
	a.usageMu.Lock()
	lastPrompt := a.lastPrompt
	a.usageMu.Unlock()
	defer func() {
		if err == nil {
			return
		}
		a.msgsMu.Lock()
		a.Messages = before
		a.msgsMu.Unlock()
		a.RestoreUsage(usage)
		a.usageMu.Lock()
		a.lastPrompt = lastPrompt
		a.usageMu.Unlock()
	}()
	if err = ctx.Err(); err != nil {
		return err
	}
	if ev.OnCompactStart != nil {
		ev.OnCompactStart(len(before), EstimateTokens(before))
	}
	summary, cutoff, info, err := a.compactHooked(ctx, compactManual)
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if ev.OnCompact != nil {
		ev.OnCompact(len(before)-len(a.Messages), len(a.Messages))
	}
	if ev.OnCompacted != nil {
		ev.OnCompacted(summary, cutoff, info)
	}
	if commit != nil {
		return commit()
	}
	return nil
}
