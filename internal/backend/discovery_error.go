package backend

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

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

const maxUpstreamMessageRunes = 200

// secretLike matches words that look like credentials, so a provider that
// echoes part of a key in its error text does not leak it to the client.
var secretLike = regexp.MustCompile(`(?i)\b(?:sk|pk|rk|ak|key|token|bearer)[-_][A-Za-z0-9_\-]{8,}|[A-Za-z0-9_\-]{32,}`)

// discoveryFailure is the 502 body of a failed model discovery. It keeps the
// generic error text and adds errorCode, plus upstreamStatus and a short
// upstreamMessage when the provider answered with an HTTP error.
func discoveryFailure(err error, p config.Provider) map[string]any {
	out := map[string]any{"version": protocol.Version, "error": "model discovery failed"}
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
		if message := upstreamMessage(httpErr.Body, p); message != "" {
			out["upstreamMessage"] = message
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
// connections, TLS failures and timeouts.
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

// upstreamMessage is the provider's own error message from a JSON error body,
// shortened and with credentials removed. Non-JSON bodies (HTML pages, proxies)
// give no message.
func upstreamMessage(body string, p config.Provider) string {
	var payload struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
		Detail  string          `json:"detail"`
	}
	if json.Unmarshal([]byte(body), &payload) != nil {
		return ""
	}
	message := payload.Message
	if len(payload.Error) > 0 {
		var text string
		var nested struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(payload.Error, &text) == nil && text != "" {
			message = text
		} else if json.Unmarshal(payload.Error, &nested) == nil && nested.Message != "" {
			message = nested.Message
		}
	}
	if message == "" {
		message = payload.Detail
	}
	message = strings.Join(strings.Fields(message), " ")
	for _, secret := range literalSecrets(p) {
		message = strings.ReplaceAll(message, secret, "[redacted]")
	}
	message = secretLike.ReplaceAllString(message, "[redacted]")
	if utf8.RuneCountInString(message) > maxUpstreamMessageRunes {
		message = string([]rune(message)[:maxUpstreamMessageRunes-1]) + "…"
	}
	return message
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
