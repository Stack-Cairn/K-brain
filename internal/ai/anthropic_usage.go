package ai

type anthropicUsage struct {
	InputTokens          int `json:"input_tokens"`
	OutputTokens         int `json:"output_tokens"`
	CacheReadInputTokens int `json:"cache_read_input_tokens"`
	CacheCreationTokens  int `json:"cache_creation_input_tokens"`
}

func (u anthropicUsage) normalized() Usage {
	return Usage{
		PromptTokens:           u.InputTokens + u.CacheReadInputTokens + u.CacheCreationTokens,
		CompletionTokens:       u.OutputTokens,
		PromptCacheHitTokens:   u.CacheReadInputTokens,
		PromptCacheWriteTokens: u.CacheCreationTokens,
	}
}
