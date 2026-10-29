package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/routing"
	"golang.org/x/net/http/httpguts"
)

func secretField(key string) bool {
	lower := strings.ToLower(strings.NewReplacer("-", "", "_", "").Replace(key))
	if strings.HasSuffix(lower, "configured") {
		return false
	}
	return strings.Contains(lower, "apikey") || strings.Contains(lower, "token") || strings.Contains(lower, "secret") || strings.Contains(lower, "password") || strings.Contains(lower, "credential") || strings.Contains(lower, "authorization") || lower == "cookie" || lower == "key" || lower == "headers" || lower == "customheaders"
}

func publicMetadata(metadata map[string]any) map[string]any {
	if metadata == nil {
		return nil
	}
	out := make(map[string]any, len(metadata))
	for key, value := range metadata {
		if secretField(key) {
			out[key] = ""
			if text, ok := value.(string); ok {
				out[key+"Configured"] = text != ""
			}
			continue
		}
		switch v := value.(type) {
		case map[string]any:
			out[key] = publicMetadata(v)
		case []any:
			items := make([]any, len(v))
			for i, item := range v {
				if m, ok := item.(map[string]any); ok {
					items[i] = publicMetadata(m)
				} else {
					items[i] = item
				}
			}
			out[key] = items
		case string:
			if strings.Contains(strings.ToLower(key), "url") {
				out[key] = publicBaseURL(v)
			} else {
				out[key] = v
			}
		default:
			out[key] = value
		}
	}
	return out
}

func validateMetadata(metadata map[string]any) error {
	for key, value := range metadata {
		if secretField(key) {
			return fmt.Errorf("credentials must not be stored in UI metadata")
		}
		switch v := value.(type) {
		case map[string]any:
			if err := validateMetadata(v); err != nil {
				return err
			}
		case []any:
			for _, item := range v {
				if m, ok := item.(map[string]any); ok {
					if err := validateMetadata(m); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func validateHeaders(headers []config.CustomHeader) error {
	seen := map[string]bool{}
	for _, header := range headers {
		key := strings.ToLower(header.Key)
		if !httpguts.ValidHeaderFieldName(header.Key) || !httpguts.ValidHeaderFieldValue(header.Value) {
			return fmt.Errorf("invalid custom header")
		}
		if seen[key] {
			return fmt.Errorf("duplicate custom header")
		}
		seen[key] = true
		if slices.Contains([]string{"host", "connection", "content-length", "transfer-encoding", "trailer", "upgrade", "proxy-authorization", "proxy-connection", "te"}, key) {
			return fmt.Errorf("transport-owned custom header is not allowed")
		}
	}
	return nil
}

func endpointURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("URL must be an HTTP(S) endpoint without user credentials or fragment")
	}
	return nil
}

func updateProvider(p config.Provider, in settingsProviderUpdate) (config.Provider, error) {
	if in.CacheCapabilities != nil {
		if err := in.CacheCapabilities.Validate(); err != nil {
			return p, err
		}
		p.CacheCapabilities = *in.CacheCapabilities
	}
	if in.CacheSessionAffinity != nil {
		p.CacheSessionAffinity = in.CacheSessionAffinity
	}
	if in.CacheControlFormat != nil {
		if *in.CacheControlFormat != "" && *in.CacheControlFormat != "anthropic" {
			return p, fmt.Errorf("unsupported cacheControlFormat")
		}
		p.CacheControlFormat = *in.CacheControlFormat
	}
	if in.PromptCacheRetention != "" && in.PromptCacheRetention != "none" && in.PromptCacheRetention != "short" && in.PromptCacheRetention != "long" {
		return p, fmt.Errorf("unsupported promptCacheRetention")
	}
	if in.Type != "" {
		if !slices.Contains([]string{"codex", "claude_code", "gemini", "xai", "deepseek"}, in.Type) {
			return p, fmt.Errorf("unsupported provider type")
		}
		p.Type = in.Type
	}
	if in.RequestFormat != "" {
		if in.RequestFormat != ai.APIChatCompletions && in.RequestFormat != ai.APIResponses {
			return p, fmt.Errorf("unsupported request format")
		}
		p.RequestFormat = in.RequestFormat
	}
	api := in.API
	if api == "" && in.Type == "" && in.RequestFormat == "" {
		api = p.API
	}
	if api == "" {
		switch p.Type {
		case "claude_code":
			api = ai.APIMessages
		case "gemini":
			api = ai.APIGemini
		case "xai":
			api = ai.APIResponses
		case "codex":
			api = p.RequestFormat
			if api == "" {
				api = ai.APIResponses
			}
		case "deepseek":
			api = ai.APIChatCompletions
		default:
			return p, fmt.Errorf("provider API or type is required")
		}
	}
	if !ai.SupportedAPI(api) {
		return p, fmt.Errorf("unsupported provider API")
	}
	p.API = api
	if in.Name != "" {
		p.Name = strings.TrimSpace(in.Name)
	}
	if in.IsFullURL != nil {
		p.IsFullURL = *in.IsFullURL
	}
	if in.BaseURL != "" {
		p.BaseURL = strings.TrimSpace(in.BaseURL)
	}
	if p.BaseURL != "" {
		if err := endpointURL(p.BaseURL); err != nil {
			return p, err
		}
	}
	if !p.IsFullURL {
		p.BaseURL = strings.TrimRight(p.BaseURL, "/")
	}
	if in.ModelsURL != nil {
		p.ModelsURL = strings.TrimSpace(*in.ModelsURL)
	}
	if p.ModelsURL != "" {
		if err := endpointURL(p.ModelsURL); err != nil {
			return p, err
		}
	}
	if in.APIKey != nil && in.Key != nil {
		return p, fmt.Errorf("use only one API key field")
	}
	keyInput := in.APIKey
	if keyInput == nil {
		keyInput = in.Key
	}
	if in.ClearAPIKey && keyInput != nil && strings.TrimSpace(*keyInput) != "" {
		return p, fmt.Errorf("cannot replace and clear an API key together")
	}
	if in.ClearAPIKey {
		p.APIKey = ""
	}
	if keyInput != nil && strings.TrimSpace(*keyInput) != "" {
		key := strings.TrimSpace(*keyInput)
		if strings.HasPrefix(key, "!") {
			return p, fmt.Errorf("API keys cannot execute commands")
		}
		p.APIKey = key
	}
	if in.CustomHeaders != nil {
		if err := validateHeaders(in.CustomHeaders); err != nil {
			return p, err
		}
		next := slices.Clone(in.CustomHeaders)
		for i, h := range next {
			if h.Value == "" && secretField(h.Key) {
				for _, old := range p.CustomHeaders {
					if strings.EqualFold(old.Key, h.Key) {
						next[i].Value = old.Value
						break
					}
				}
			}
		}
		p.CustomHeaders = next
	}
	if in.ActiveModels != nil {
		p.ActiveModels = slices.Clone(in.ActiveModels)
	}
	if in.ModelOrder != nil {
		p.ModelOrder = slices.Clone(in.ModelOrder)
	}
	if in.Reasoning != "" {
		p.Reasoning = in.Reasoning
	}
	if in.PromptCachingEnabled != nil {
		p.PromptCachingEnabled = in.PromptCachingEnabled
	}
	if in.PromptCacheHintMode != "" {
		p.PromptCacheHintMode = in.PromptCacheHintMode
	}
	if in.PromptCacheRetention != "" {
		p.PromptCacheRetention = in.PromptCacheRetention
	}
	if in.NativeWebSearchEnabled != nil {
		p.NativeWebSearchEnabled = *in.NativeWebSearchEnabled
	}
	if in.UseSystemProxy != nil {
		p.UseSystemProxy = *in.UseSystemProxy
	}
	if in.RetryPolicy != nil {
		p.RetryPolicy = in.RetryPolicy
	}
	if in.Metadata != nil {
		if err := validateMetadata(in.Metadata); err != nil {
			return p, err
		}
		p.Metadata = in.Metadata
	}
	if in.UsageQuery != nil {
		for _, key := range []string{"apiKey", "accessToken", "secretAccessKey"} {
			if value, ok := in.UsageQuery[key].(string); ok && value == "" && in.UsageQuery[key+"Configured"] == true {
				in.UsageQuery[key] = p.UsageQuery[key]
			}
		}
		p.UsageQuery = in.UsageQuery
	}
	return p, nil
}

func providerModelsID(path string) (string, error) {
	const prefix = "/v1/settings/providers/"
	const suffix = "/models"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return "", fmt.Errorf("provider ID is required")
	}
	escapedID := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	if escapedID == "" {
		return "", fmt.Errorf("provider ID is required")
	}
	id, err := url.PathUnescape(escapedID)
	if err != nil || id == "" {
		return "", fmt.Errorf("provider ID is required")
	}
	return id, nil
}

func (s *Server) handleProviderModels(w http.ResponseWriter, r *http.Request) {
	if s.settings == nil {
		writeJSONError(w, 501, "backend settings are unavailable")
		return
	}
	id, err := providerModelsID(r.URL.EscapedPath())
	if err != nil {
		writeJSONError(w, 400, err.Error())
		return
	}
	cfg := s.settings.Snapshot()
	p, exists := cfg.Providers[id]
	var draft struct {
		settingsProviderUpdate
		ProviderID string `json:"providerId,omitempty"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()
	decodeErr := decoder.Decode(&draft)
	if decodeErr != nil && decodeErr != io.EOF {
		writeJSONError(w, 400, "invalid discovery request")
		return
	}
	if decodeErr == nil {
		if err := decoder.Decode(new(any)); err != io.EOF {
			writeJSONError(w, 400, "expected one discovery object")
			return
		}
	}
	if draft.ProviderID != "" && draft.ProviderID != id {
		writeJSONError(w, 400, "provider ID mismatch")
		return
	}
	if !exists && draft.BaseURL == "" {
		writeJSONError(w, 404, "provider not found")
		return
	}
	// Stored credentials may not be forwarded to a different draft endpoint.
	if exists && (draft.BaseURL != "" && draft.BaseURL != p.BaseURL || draft.ModelsURL != nil && *draft.ModelsURL != p.ModelsURL) {
		p.APIKey = ""
		p.CustomHeaders = nil
	}
	p, err = updateProvider(p, draft.settingsProviderUpdate)
	if err != nil {
		writeJSONError(w, 400, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	client, err := routing.ClientForProviderContext(ctx, p, id, 1)
	if err != nil {
		writeJSONError(w, 400, "provider credentials or endpoint are not configured")
		return
	}
	models, err := client.Models(ctx)
	if err != nil {
		writeJSONError(w, 502, modelDiscoveryError(err))
		return
	}
	out := make([]settingsModel, 0, len(models))
	for _, m := range models {
		mv := settingsModel{Provider: id, ID: m.ID, ContextWindow: m.ContextLength, MaxOutputTokens: m.MaxCompletionTokens, MaxOutputToken: m.MaxCompletionTokens, InputModalities: m.InputModalities, OutputModalities: m.OutputModalities, Vision: m.SupportsVision()}
		// Same result as GET /v1/settings for a stored model; a new one uses discovery metadata.
		meta := config.Model{OutputModalities: m.OutputModalities}
		if shared, ok := cfg.Models[m.ID]; ok && slices.Contains(shared.Providers, id) {
			meta = shared.ForProvider(id)
		}
		_, mv.ImageModel = imageModelAdapter(p, m.ID, meta)
		out = append(out, mv)
	}
	writeJSON(w, 200, map[string]any{"version": protocol.Version, "provider": id, "models": out})
}

func modelDiscoveryError(err error) string {
	const prefix = "model discovery failed: "
	// Upstream bodies and network errors may contain credentials or private URLs.
	var upstream *ai.HTTPError
	if errors.As(err, &upstream) {
		switch upstream.StatusCode {
		case http.StatusUnauthorized:
			return prefix + "provider rejected the API key (HTTP 401)"
		case http.StatusForbidden:
			return prefix + "provider denied access to models (HTTP 403)"
		case http.StatusNotFound, http.StatusMethodNotAllowed:
			return prefix + fmt.Sprintf("model endpoint unavailable (HTTP %d); check Base URL and Models URL", upstream.StatusCode)
		case http.StatusTooManyRequests:
			return prefix + "provider rate limit reached (HTTP 429); retry later"
		default:
			return prefix + fmt.Sprintf("provider returned HTTP %d", upstream.StatusCode)
		}
	}
	var network net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &network) && network.Timeout() {
		return prefix + "request timed out; check the connection and proxy settings"
	}
	if errors.As(err, &network) {
		return prefix + "could not connect to provider; check Base URL and proxy settings"
	}
	return "model discovery failed"
}
