package config

import "github.com/Stack-Cairn/K-brain/internal/ai"

// UsageCost prices usage of model on provider prov with the model catalogs'
// rates, in USD. It is the cost logic of the kn /usage card and the backend's
// usage statistics. With prov empty the model must appear in exactly one
// catalog. ok is false when the route has no known price (or the usage is
// inconsistent); usage of zero tokens always costs 0.
func UsageCost(cats map[string]Catalog, model, prov string, u ai.Usage) (cost float64, ok bool) {
	if u.PromptTokens == 0 && u.CompletionTokens == 0 {
		c, valid := ai.CalculateCost(u, ai.TokenRates{})
		return c.Total, valid
	}
	if cat, ok := cats[prov]; ok {
		if rates, ok := cat.Pricing(model); ok {
			c, valid := ai.CalculateCost(u, rates)
			return c.Total, valid
		}
		return 0, false
	}
	if prov != "" {
		return 0, false
	}
	found := false
	for _, cat := range cats {
		if cat.Find(model) != nil {
			if found {
				return 0, false
			}
			rates, ok := cat.Pricing(model)
			if !ok {
				return 0, false
			}
			value, valid := ai.CalculateCost(u, rates)
			if !valid {
				return 0, false
			}
			cost, found = value.Total, true
		}
	}
	return cost, found
}

// SubscriptionBilled reports whether provider name is billed by subscription
// rather than per token, so catalog prices do not describe what it costs.
func (c *Config) SubscriptionBilled(name string) bool {
	return c != nil && c.Providers[name].API == "openai-codex-responses"
}
