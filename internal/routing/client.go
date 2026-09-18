package routing

import (
	"fmt"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
)

func ClientForProvider(prov config.Provider, name string, maxRetries int) (ai.Client, error) {
	if prov.API != "" && prov.API != "openai-completions" && prov.API != "openai-responses" && prov.API != "anthropic-messages" {
		return nil, fmt.Errorf("unsupported API %q for provider %q", prov.API, name)
	}
	key, err := prov.ResolveKey()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(key) == "" {
		return nil, fmt.Errorf("no API key for provider %q (set apiKey in ~/.k-brain/config.json)", name)
	}
	client, err := ai.NewClient(ai.ClientOptions{API: prov.API, BaseURL: prov.BaseURL, APIKey: key, MaxRetries: maxRetries})
	if err != nil {
		return nil, fmt.Errorf("provider %q: %w", name, err)
	}
	configureCache(client, prov)
	return client, nil
}

func ClientForProviderOptional(prov config.Provider, name string, maxRetries int) (ai.Client, error) {
	if strings.TrimSpace(prov.BaseURL) == "" || strings.TrimSpace(prov.APIKey) == "" {
		client := ai.New(prov.BaseURL, "")
		client.MaxRetries = maxRetries
		configureCache(client, prov)
		return client, nil
	}
	return ClientForProvider(prov, name, maxRetries)
}
func configureCache(client ai.Client, prov config.Provider) {
	o, ok := client.(interface{ SetCacheOptions(ai.CacheOptions) })
	if !ok {
		return
	}
	retention := prov.PromptCacheRetention
	if retention == "" {
		retention = "short"
	}
	if prov.PromptCachingEnabled != nil && !*prov.PromptCachingEnabled {
		retention = "none"
	}
	affinity := prov.CacheSessionAffinity != nil && *prov.CacheSessionAffinity
	o.SetCacheOptions(ai.CacheOptions{Retention: retention, SessionAffinity: affinity, ControlFormat: prov.CacheControlFormat, SupportsLong: strings.Contains(strings.ToLower(prov.BaseURL), "api.openai.com")})
}
