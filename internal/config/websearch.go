package config

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
)

// Web search providers the web_search tool can call.
const (
	WebSearchTavily  = "tavily"
	WebSearchBrave   = "brave"
	WebSearchSearXNG = "searxng"
	WebSearchBocha   = "bocha"
	WebSearchJina    = "jina"
)

// WebSearchProviders lists the supported webSearch.provider values.
var WebSearchProviders = []string{WebSearchTavily, WebSearchBrave, WebSearchSearXNG, WebSearchBocha, WebSearchJina}

const (
	// WebSearchDefaultResults is how many results a search returns unless the
	// configuration or the call asks for another count.
	WebSearchDefaultResults = 5
	// WebSearchMaxResults caps every search, whatever the configuration or call asks.
	WebSearchMaxResults = 10
)

// Environment variables that override the webSearch section. The general key
// variable wins over the provider-specific ones.
const (
	EnvWebSearchProvider = "K_BRAIN_WEB_SEARCH_PROVIDER"
	EnvWebSearchAPIKey   = "K_BRAIN_WEB_SEARCH_API_KEY"
	EnvWebSearchBaseURL  = "K_BRAIN_WEB_SEARCH_BASE_URL"
)

// webSearchProviderKeyEnv is each provider's own API key variable.
var webSearchProviderKeyEnv = map[string]string{
	WebSearchTavily: "TAVILY_API_KEY",
	WebSearchBrave:  "BRAVE_SEARCH_API_KEY",
	WebSearchBocha:  "BOCHA_API_KEY",
	WebSearchJina:   "JINA_API_KEY",
}

// WebSearchKeyEnv names the provider-specific API key variable, or "" when the
// provider has none.
func WebSearchKeyEnv(provider string) string { return webSearchProviderKeyEnv[provider] }

// WebSearchConfig is the webSearch section: the third-party search service the
// web_search tool calls when web search is on and the model's API cannot search
// natively.
//
//	provider:   tavily, brave, searxng, bocha or jina
//	apiKey:     the service's key; like provider keys it may be $ENV, ${ENV} or !command
//	baseUrl:    the service root (required for searxng, optional override for the others)
//	maxResults: results per search (default 5, at most 10)
type WebSearchConfig struct {
	Provider   string `json:"provider,omitempty"`
	APIKey     string `json:"apiKey,omitempty"`
	BaseURL    string `json:"baseUrl,omitempty"`
	MaxResults int    `json:"maxResults,omitempty"`
}

// Validate checks the stored section; environment overrides are judged by
// Effective, which reports a problem instead of failing the configuration.
func (w WebSearchConfig) Validate() error {
	if p := strings.TrimSpace(w.Provider); p != "" && !slices.Contains(WebSearchProviders, p) {
		return fmt.Errorf("webSearch.provider must be one of %s", strings.Join(WebSearchProviders, ", "))
	}
	if w.MaxResults < 0 {
		return fmt.Errorf("webSearch.maxResults must be non-negative")
	}
	if strings.TrimSpace(w.BaseURL) != "" && !validWebSearchURL(w.BaseURL) {
		return fmt.Errorf("webSearch.baseUrl must be an http or https URL")
	}
	return nil
}

func validWebSearchURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https")
}

// Key sources reported by WebSearchEffective.KeySource.
const (
	WebSearchKeyFromEnv    = "env"
	WebSearchKeyFromConfig = "config"
)

// WebSearchEffective is the webSearch section with environment overrides applied.
// The key is kept as written (a secret reference is resolved only when a search
// runs), so building it never runs a command.
type WebSearchEffective struct {
	Provider   string
	BaseURL    string
	MaxResults int
	// KeySource is WebSearchKeyFromEnv, WebSearchKeyFromConfig or "" (no key).
	KeySource string
	// Problem says why the web_search tool cannot be offered; "" when it can.
	Problem string

	key string
}

// Available reports whether the web_search tool can be offered.
func (e WebSearchEffective) Available() bool { return e.Problem == "" }

// KeyConfigured reports whether an API key was found in the environment or the configuration.
func (e WebSearchEffective) KeyConfigured() bool { return e.key != "" }

// ResolveKey returns the API key, resolving a $ENV, ${ENV} or !command reference.
// Errors never contain the key.
func (e WebSearchEffective) ResolveKey(ctx context.Context) (string, error) {
	if e.key == "" || e.KeySource == WebSearchKeyFromEnv {
		return e.key, nil
	}
	key, err := ResolveSecretContext(ctx, e.key)
	if err != nil {
		if IsWholeRef(e.key) {
			return "", fmt.Errorf("webSearch.apiKey: %w", err)
		}
		return "", fmt.Errorf("webSearch.apiKey could not be resolved")
	}
	if strings.TrimSpace(key) == "" {
		return "", fmt.Errorf("webSearch.apiKey resolved to an empty value")
	}
	return key, nil
}

// Effective applies the K_BRAIN_WEB_SEARCH_* and provider key variables over the
// section and says whether the result is usable. Precedence for the key:
// K_BRAIN_WEB_SEARCH_API_KEY, then the provider's own variable (TAVILY_API_KEY,
// BRAVE_SEARCH_API_KEY, BOCHA_API_KEY, JINA_API_KEY), then webSearch.apiKey.
func (w WebSearchConfig) Effective() WebSearchEffective {
	return w.effective(os.Getenv)
}

func (w WebSearchConfig) effective(getenv func(string) string) WebSearchEffective {
	e := WebSearchEffective{
		Provider:   strings.ToLower(strings.TrimSpace(w.Provider)),
		BaseURL:    strings.TrimSpace(w.BaseURL),
		MaxResults: w.MaxResults,
	}
	if v := strings.TrimSpace(getenv(EnvWebSearchProvider)); v != "" {
		e.Provider = strings.ToLower(v)
	}
	if v := strings.TrimSpace(getenv(EnvWebSearchBaseURL)); v != "" {
		e.BaseURL = v
	}
	if e.MaxResults <= 0 {
		e.MaxResults = WebSearchDefaultResults
	}
	e.MaxResults = min(e.MaxResults, WebSearchMaxResults)
	if v := strings.TrimSpace(getenv(EnvWebSearchAPIKey)); v != "" {
		e.key, e.KeySource = v, WebSearchKeyFromEnv
	} else if name := webSearchProviderKeyEnv[e.Provider]; name != "" && strings.TrimSpace(getenv(name)) != "" {
		e.key, e.KeySource = strings.TrimSpace(getenv(name)), WebSearchKeyFromEnv
	} else if v := strings.TrimSpace(w.APIKey); v != "" {
		e.key, e.KeySource = v, WebSearchKeyFromConfig
	}
	switch {
	case e.Provider == "":
		e.Problem = "no web search provider is configured (set webSearch.provider to one of " + strings.Join(WebSearchProviders, ", ") + ")"
	case !slices.Contains(WebSearchProviders, e.Provider):
		e.Problem = fmt.Sprintf("unsupported web search provider %q (use one of %s)", e.Provider, strings.Join(WebSearchProviders, ", "))
	case e.BaseURL != "" && !validWebSearchURL(e.BaseURL):
		e.Problem = "webSearch.baseUrl must be an http or https URL"
	case e.Provider == WebSearchSearXNG && e.BaseURL == "":
		e.Problem = "webSearch.baseUrl is required for searxng"
	case e.Provider != WebSearchSearXNG && e.key == "":
		e.Problem = fmt.Sprintf("no API key for %s (set webSearch.apiKey, %s or %s)", e.Provider, webSearchProviderKeyEnv[e.Provider], EnvWebSearchAPIKey)
	}
	return e
}
