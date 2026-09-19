package agent

import "github.com/Stack-Cairn/K-brain/internal/ai"

func (a *Agent) UsageSummary() ai.UsageSummary {
	a.usageMu.Lock()
	defer a.usageMu.Unlock()
	return ai.UsageSummary{Total: copyUsage(a.usage), Models: copyUsageMap(a.modelUsage), Subagents: copyUsageMap(a.subUsage)}
}

func (a *Agent) RestoreUsage(usage ai.UsageSummary) {
	a.usageMu.Lock()
	defer a.usageMu.Unlock()
	a.usage = copyUsage(usage.Total)
	a.modelUsage = copyUsageMap(usage.Models)
	a.subUsage = copyUsageMap(usage.Subagents)
	a.lastPrompt = 0
}

func (a *Agent) ModelUsage() map[string]ai.Usage {
	a.usageMu.Lock()
	defer a.usageMu.Unlock()
	return copyUsageMap(a.modelUsage)
}

func (a *Agent) SetModelUsage(usage map[string]ai.Usage) {
	if len(usage) == 0 {
		return
	}
	a.usageMu.Lock()
	defer a.usageMu.Unlock()
	a.modelUsage = copyUsageMap(usage)
}
