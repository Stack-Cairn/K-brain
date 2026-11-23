package backend

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
)

// Codes of a failed model discovery, sent as errorCode next to the generic error.
const (
	discoveryInvalidAPIKey = "invalid_api_key"
	discoveryNotFound      = "not_found"
	discoveryUnreachable   = "unreachable"
	discoveryRateLimited   = "rate_limited"
	discoveryUpstreamError = "upstream_error"
)

// discoveryFailure is the 502 body of a failed model discovery. It keeps the
// generic error text and adds errorCode, plus upstreamStatus and a short
// upstreamMessage when the provider answered with an HTTP error.
func discoveryFailure(err error, p config.Provider) map[string]any {
	out := map[string]any{"version": protocol.Version, "error": "model discovery failed"}
	detail := ai.ClassifyError(err, literalSecrets(p)...)
	var httpErr *ai.HTTPError
	if errors.As(err, &httpErr) {
		out["upstreamStatus"] = httpErr.StatusCode
		switch httpErr.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			out["errorCode"] = discoveryInvalidAPIKey
		case http.StatusNotFound:
			out["errorCode"] = discoveryNotFound
		case http.StatusTooManyRequests:
			out["errorCode"] = discoveryRateLimited
		default:
			out["errorCode"] = discoveryUpstreamError
		}
		if detail.ProviderMessage != "" {
			out["upstreamMessage"] = detail.ProviderMessage
		}
		return out
	}
	out["errorCode"] = discoveryUpstreamError
	if unreachable(err) {
		out["errorCode"] = discoveryUnreachable
	}
	return out
}

// unreachable reports a failure to get any HTTP answer: DNS, refused or reset
// connections, TLS failures and timeouts. A body cut short after the answer
// (a truncated catalog) is an upstream error instead.
func unreachable(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var urlErr *url.Error
	return errors.As(err, &urlErr)
}

// literalSecrets lists the literal key and header values of a provider that
// are long enough to be worth redacting.
func literalSecrets(p config.Provider) []string {
	var out []string
	if len(p.APIKey) >= 8 {
		out = append(out, p.APIKey)
	}
	for _, h := range p.CustomHeaders {
		if len(h.Value) >= 8 {
			out = append(out, h.Value)
		}
	}
	return out
}

// providerLiteralSecrets lists the literal secrets of a configured provider,
// for redacting its error messages.
func (s *Server) providerLiteralSecrets(provider string) []string {
	if s.settings == nil {
		return nil
	}
	cfg := s.settings.Snapshot()
	if cfg == nil {
		return nil
	}
	return literalSecrets(cfg.Providers[provider])
}
