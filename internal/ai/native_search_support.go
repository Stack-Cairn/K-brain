package ai

import "strings"

// NativeWebSearchSupporter is implemented by clients that know whether their API
// runs provider-native web search. Wrappers forward to the client they route to.
type NativeWebSearchSupporter interface {
	SupportsNativeWebSearch() bool
}

// SupportsNativeWebSearch reports whether a request with NativeWebSearch set is
// served by the provider's own search. Clients that do not say are treated as
// unable, so a configured fallback search tool can stand in for them.
func SupportsNativeWebSearch(c Client) bool {
	if s, ok := c.(NativeWebSearchSupporter); ok {
		return s.SupportsNativeWebSearch()
	}
	return false
}

// SupportsNativeWebSearchForModel includes model-specific extensions such as
// DeepSeek Flash's Chat Completions tools. API-only settings remain conservative.
func SupportsNativeWebSearchForModel(c Client, model string) bool {
	// Responses embeds OpenAI; its API-wide support must win over the
	// embedded Chat Completions model extension.
	if SupportsNativeWebSearch(c) {
		return true
	}
	if s, ok := c.(interface{ SupportsNativeWebSearchForModel(string) bool }); ok {
		return s.SupportsNativeWebSearchForModel(model)
	}
	return false
}

func (c *OpenAI) SupportsNativeWebSearchForModel(model string) bool {
	return deepSeekWebToolsModel(model)
}

// APISupportsNativeWebSearch is SupportsNativeWebSearch for a provider setting
// that has no client yet: the API family and, for Gemini, the endpoint.
func APISupportsNativeWebSearch(api, baseURL string) bool {
	switch strings.TrimSpace(api) {
	case APIResponses, APIMessages:
		return true
	case APIGemini, APIGoogleGemini:
		return isOfficialGeminiEndpoint(strings.TrimSpace(baseURL))
	}
	return false
}

// Chat Completions has no native search request shape.
func (c *OpenAI) SupportsNativeWebSearch() bool { return false }

func (c *Responses) SupportsNativeWebSearch() bool { return true }

func (c *Responses) SupportsNativeWebSearchForModel(string) bool { return true }

func (c *Anthropic) SupportsNativeWebSearch() bool { return true }

// Gemini relays drop google_search whenever function tools are present, so only
// the official endpoint counts as serving it.
func (c *Gemini) SupportsNativeWebSearch() bool { return isOfficialGeminiEndpoint(c.BaseURL) }
