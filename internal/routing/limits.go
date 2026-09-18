package routing

import "github.com/Stack-Cairn/K-brain/internal/config"

func TokenLimits(mdl config.Model, cat config.Catalog, apiID string) (contextLimit, maxOut int) {
	contextLimit = mdl.ContextWindow()
	if contextLimit <= 0 {
		contextLimit = max(0, cat.ContextLength(apiID))
	}
	maxOut = mdl.MaxOut
	if maxOut <= 0 {
		maxOut = cat.MaxCompletionTokens(apiID)
	}
	if maxOut <= 0 {
		maxOut = contextLimit
	}
	return contextLimit, maxOut
}
