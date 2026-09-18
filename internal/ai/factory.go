package ai

import (
	"fmt"
	"net/url"
	"strings"
)

const (
	APIChatCompletions = "openai-completions"
	APIResponses       = "openai-responses"
	APIMessages        = "anthropic-messages"
)

type ClientOptions struct {
	API        string
	BaseURL    string
	APIKey     string
	MaxRetries int
	Cache      CacheOptions
}

func NewClient(opts ClientOptions) (Client, error) {
	api := strings.TrimSpace(opts.API)
	if api == "" {
		api = APIChatCompletions
	}
	u, err := url.Parse(strings.TrimSpace(opts.BaseURL))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("invalid baseUrl: set an http/https API endpoint")
	}
	if strings.TrimSpace(opts.APIKey) == "" {
		return nil, fmt.Errorf("API key is required")
	}
	var client Client
	switch api {
	case APIChatCompletions:
		client = New(opts.BaseURL, opts.APIKey)
	case APIResponses:
		client = NewResponses(opts.BaseURL, opts.APIKey)
	case APIMessages:
		client = NewAnthropic(opts.BaseURL, opts.APIKey)
	default:
		return nil, fmt.Errorf("unsupported API %q", api)
	}
	if setter, ok := client.(interface{ SetMaxRetries(int) }); ok {
		setter.SetMaxRetries(opts.MaxRetries)
	}
	if setter, ok := client.(interface{ SetCacheOptions(CacheOptions) }); ok {
		setter.SetCacheOptions(opts.Cache)
	}
	return client, nil
}
