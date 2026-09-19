package session

import "github.com/Stack-Cairn/K-brain/internal/ai"

func (m Meta) UsageSummary(msgs []ai.Message) ai.UsageSummary {
	u := ai.Usage{PromptTokens: m.UsageIn, CompletionTokens: m.UsageOut, PromptCacheHitTokens: m.UsageCached, PromptCacheWriteTokens: m.UsageCacheWrite}
	models := m.ModelUsage
	if u.PromptTokens == 0 && u.CompletionTokens == 0 && len(m.ModelUsage) == 0 {
		models = make(map[string]ai.Usage)
		for _, msg := range msgs {
			if msg.Usage != nil {
				u.Add(*msg.Usage)
				label := msg.Model
				if label == "" {
					label = m.Model + " @ " + m.Provider
				}
				usage := models[label]
				usage.Add(*msg.Usage)
				models[label] = usage
			}
		}
	}
	if len(models) == 0 && (u.PromptTokens > 0 || u.CompletionTokens > 0) {
		models = map[string]ai.Usage{m.Model + " @ " + m.Provider: u}
	}
	return ai.UsageSummary{Total: u, Models: models, Subagents: m.SubUsage}
}

func (m *Meta) setUsage(usage ai.UsageSummary) {
	m.UsageIn, m.UsageCached, m.UsageOut = usage.Total.PromptTokens, usage.Total.Cached(), usage.Total.CompletionTokens
	m.UsageCacheWrite = usage.Total.CacheWrite()
	m.ModelUsage, m.SubUsage = usage.Models, usage.Subagents
}
