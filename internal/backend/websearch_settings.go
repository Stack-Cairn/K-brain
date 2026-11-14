package backend

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// settingsWebSearch is the webSearch section of GET /v1/settings (capability
// web_search_fallback). provider, baseUrl and maxResults are the stored values;
// the other fields describe the effective setting after environment overrides.
// The API key itself is never returned.
type settingsWebSearch struct {
	Provider   string `json:"provider"`
	BaseURL    string `json:"baseUrl,omitempty"`
	MaxResults int    `json:"maxResults"`
	// APIKeyConfigured is true when a key is stored or set in the environment;
	// APIKeySource says which ("config" or "env").
	APIKeyConfigured bool   `json:"apiKeyConfigured"`
	APIKeySource     string `json:"apiKeySource,omitempty"`
	// Available is true when runs that enable search on a provider without
	// native search get the web_search tool; Reason says why not otherwise.
	Available bool     `json:"available"`
	Reason    string   `json:"reason,omitempty"`
	Providers []string `json:"providers"`
}

// settingsWebSearchUpdate is the webSearch section of PUT /v1/settings. Omitted
// fields keep their value; apiKey "" keeps the stored key and clearApiKey
// removes it. The read-only fields are accepted so a GET body can be PUT back.
type settingsWebSearchUpdate struct {
	Provider    *string `json:"provider,omitempty"`
	APIKey      *string `json:"apiKey,omitempty"`
	ClearAPIKey bool    `json:"clearApiKey,omitempty"`
	BaseURL     *string `json:"baseUrl,omitempty"`
	MaxResults  *int    `json:"maxResults,omitempty"`

	APIKeyConfigured *bool    `json:"apiKeyConfigured,omitempty"`
	APIKeySource     *string  `json:"apiKeySource,omitempty"`
	Available        *bool    `json:"available,omitempty"`
	Reason           *string  `json:"reason,omitempty"`
	Providers        []string `json:"providers,omitempty"`
}

func projectWebSearch(w config.WebSearchConfig) settingsWebSearch {
	eff := w.Effective()
	return settingsWebSearch{
		Provider:         strings.TrimSpace(w.Provider),
		BaseURL:          publicBaseURL(w.BaseURL),
		MaxResults:       w.MaxResults,
		APIKeyConfigured: eff.KeyConfigured(),
		APIKeySource:     eff.KeySource,
		Available:        eff.Available(),
		Reason:           eff.Problem,
		Providers:        slices.Clone(config.WebSearchProviders),
	}
}

func applyWebSearchUpdate(cfg *config.Config, in settingsWebSearchUpdate) error {
	next := cfg.WebSearch
	if in.Provider != nil {
		next.Provider = strings.ToLower(strings.TrimSpace(*in.Provider))
	}
	if in.BaseURL != nil {
		edited := strings.TrimSpace(*in.BaseURL)
		if !(next.BaseURL != "" && edited == publicBaseURL(next.BaseURL)) {
			next.BaseURL = edited
		}
	}
	if in.MaxResults != nil {
		if *in.MaxResults < 0 || *in.MaxResults > config.WebSearchMaxResults {
			return fmt.Errorf("webSearch.maxResults must be between 0 and %d", config.WebSearchMaxResults)
		}
		next.MaxResults = *in.MaxResults
	}
	key := ""
	if in.APIKey != nil {
		key = strings.TrimSpace(*in.APIKey)
	}
	if in.ClearAPIKey && key != "" {
		return fmt.Errorf("cannot replace and clear the web search API key together")
	}
	if in.ClearAPIKey {
		next.APIKey = ""
	}
	if key != "" {
		if strings.HasPrefix(key, "!") {
			return fmt.Errorf("API keys cannot execute commands")
		}
		next.APIKey = key
	}
	if err := next.Validate(); err != nil {
		return err
	}
	cfg.WebSearch = next
	return nil
}

// attachWebSearch gives a session agent the fallback web_search tool, reading
// the live settings so a saved webSearch section applies to the next request.
func (s *Server) attachWebSearch(ag *agent.Agent) {
	if s.settings == nil {
		return
	}
	ag.WebSearchFallback = tools.WebSearchFallback(s.settings.WebSearch)
}
