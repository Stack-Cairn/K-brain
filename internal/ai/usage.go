package ai

type UsageSummary struct {
	Total     Usage
	Models    map[string]Usage
	Subagents map[string]Usage
}

func (u *Usage) Add(other Usage) {
	cached := u.Cached() + other.Cached()
	u.PromptTokens += other.PromptTokens
	u.CompletionTokens += other.CompletionTokens
	u.PromptCacheHitTokens += other.PromptCacheHitTokens
	u.PromptCacheWriteTokens += other.PromptCacheWriteTokens
	if cached > 0 || u.PromptTokensDetails != nil || other.PromptTokensDetails != nil {
		u.PromptTokensDetails = &struct {
			CachedTokens int `json:"cached_tokens"`
		}{CachedTokens: cached}
	}
}
