package config

import (
	"context"
	"strings"
	"testing"
)

func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestWebSearchConfigValidate(t *testing.T) {
	cases := []struct {
		name string
		in   WebSearchConfig
		want string
	}{
		{"empty", WebSearchConfig{}, ""},
		{"tavily", WebSearchConfig{Provider: "tavily", APIKey: "fixture-key", MaxResults: 8}, ""},
		{"searxng", WebSearchConfig{Provider: "searxng", BaseURL: "http://127.0.0.1:8888"}, ""},
		{"unknown provider", WebSearchConfig{Provider: "altavista"}, "webSearch.provider must be one of"},
		{"negative results", WebSearchConfig{Provider: "brave", MaxResults: -1}, "webSearch.maxResults"},
		{"bad scheme", WebSearchConfig{Provider: "searxng", BaseURL: "ftp://search.example"}, "webSearch.baseUrl"},
		{"no host", WebSearchConfig{Provider: "searxng", BaseURL: "search.example"}, "webSearch.baseUrl"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.in.Validate()
			if tc.want == "" {
				if err != nil {
					t.Fatalf("Validate() = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestWebSearchEffectiveKeyPrecedence(t *testing.T) {
	cfg := WebSearchConfig{Provider: "tavily", APIKey: "config-key"}

	e := cfg.effective(envMap(nil))
	if !e.Available() || e.KeySource != WebSearchKeyFromConfig || e.key != "config-key" {
		t.Fatalf("config key: %+v", e)
	}
	e = cfg.effective(envMap(map[string]string{"TAVILY_API_KEY": "provider-env"}))
	if e.KeySource != WebSearchKeyFromEnv || e.key != "provider-env" {
		t.Fatalf("provider env should win over config: %+v", e)
	}
	e = cfg.effective(envMap(map[string]string{"TAVILY_API_KEY": "provider-env", EnvWebSearchAPIKey: "general-env"}))
	if e.KeySource != WebSearchKeyFromEnv || e.key != "general-env" {
		t.Fatalf("general env should win: %+v", e)
	}
	// Another provider's variable does not apply.
	e = WebSearchConfig{Provider: "brave"}.effective(envMap(map[string]string{"TAVILY_API_KEY": "provider-env"}))
	if e.Available() || e.KeyConfigured() || !strings.Contains(e.Problem, "BRAVE_SEARCH_API_KEY") {
		t.Fatalf("brave without key: %+v", e)
	}
	if strings.Contains(e.Problem, "provider-env") {
		t.Fatal("problem leaks a key")
	}
}

func TestWebSearchEffectiveEnvOverrides(t *testing.T) {
	e := WebSearchConfig{}.effective(envMap(map[string]string{
		EnvWebSearchProvider: " SearXNG ",
		EnvWebSearchBaseURL:  "http://127.0.0.1:8080",
	}))
	if !e.Available() || e.Provider != WebSearchSearXNG || e.BaseURL != "http://127.0.0.1:8080" {
		t.Fatalf("env-only searxng: %+v", e)
	}
	if e.KeyConfigured() {
		t.Fatal("searxng needs no key")
	}
	if e.MaxResults != WebSearchDefaultResults {
		t.Fatalf("default results = %d", e.MaxResults)
	}
	if got := (WebSearchConfig{Provider: "jina", APIKey: "k", MaxResults: 50}).effective(envMap(nil)).MaxResults; got != WebSearchMaxResults {
		t.Fatalf("results cap = %d", got)
	}
}

func TestWebSearchEffectiveProblems(t *testing.T) {
	cases := []struct {
		cfg  WebSearchConfig
		env  map[string]string
		want string
	}{
		{WebSearchConfig{}, nil, "no web search provider"},
		{WebSearchConfig{Provider: "bocha"}, map[string]string{EnvWebSearchProvider: "nope"}, "unsupported web search provider"},
		{WebSearchConfig{Provider: "searxng"}, nil, "baseUrl is required for searxng"},
		{WebSearchConfig{Provider: "jina", APIKey: "k"}, map[string]string{EnvWebSearchBaseURL: "not a url"}, "http or https"},
		{WebSearchConfig{Provider: "bocha"}, nil, "no API key for bocha"},
	}
	for _, tc := range cases {
		e := tc.cfg.effective(envMap(tc.env))
		if e.Available() || !strings.Contains(e.Problem, tc.want) {
			t.Errorf("%+v / %v: problem %q, want %q", tc.cfg, tc.env, e.Problem, tc.want)
		}
	}
}

func TestWebSearchResolveKey(t *testing.T) {
	t.Setenv("KB_TEST_SEARCH_KEY", "from-ref")
	e := WebSearchConfig{Provider: "tavily", APIKey: "$KB_TEST_SEARCH_KEY"}.effective(envMap(nil))
	key, err := e.ResolveKey(context.Background())
	if err != nil || key != "from-ref" {
		t.Fatalf("ResolveKey = %q, %v", key, err)
	}

	e = WebSearchConfig{Provider: "tavily", APIKey: "${KB_TEST_SEARCH_MISSING}"}.effective(envMap(nil))
	if _, err := e.ResolveKey(context.Background()); err == nil || !strings.Contains(err.Error(), "webSearch.apiKey") {
		t.Fatalf("missing ref error = %v", err)
	}

	// Keys from the environment are used as written, never resolved.
	e = WebSearchConfig{Provider: "tavily"}.effective(envMap(map[string]string{EnvWebSearchAPIKey: "$NOT_A_REF"}))
	if key, err := e.ResolveKey(context.Background()); err != nil || key != "$NOT_A_REF" {
		t.Fatalf("env key = %q, %v", key, err)
	}

	// A failing command never echoes the key text.
	// The command itself exits non-zero on every platform (or is missing on
	// Windows); its argument stands in for secret text that must not leak.
	secret := "!false leaked-secret-value"
	e = WebSearchConfig{Provider: "tavily", APIKey: secret}.effective(envMap(nil))
	_, err = e.ResolveKey(context.Background())
	if err == nil {
		t.Fatal("failing command resolved")
	}
	if strings.Contains(err.Error(), "leaked-secret-value") {
		t.Fatalf("error leaks the key: %v", err)
	}
}

func TestParseConfigWebSearchSection(t *testing.T) {
	var cfg Config
	err := parseConfigJSONC([]byte(`{
		// third-party search for APIs without native search
		"providers": {},
		"webSearch": {"provider": "brave", "apiKey": "$BRAVE_SEARCH_API_KEY", "maxResults": 7}
	}`), &cfg)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WebSearch.Provider != "brave" || cfg.WebSearch.APIKey != "$BRAVE_SEARCH_API_KEY" || cfg.WebSearch.MaxResults != 7 {
		t.Fatalf("webSearch = %+v", cfg.WebSearch)
	}

	err = parseConfigJSONC([]byte(`{"providers": {}, "webSearch": {"provider": "nope"}}`), &cfg)
	if err == nil || !strings.Contains(err.Error(), "webSearch.provider") {
		t.Fatalf("invalid provider error = %v", err)
	}
	err = parseConfigJSONC([]byte(`{"providers": {}, "webSearch": {"provider": "brave", "token": "x"}}`), &cfg)
	if err == nil {
		t.Fatal("unknown webSearch field accepted")
	}
}
