package ai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"syscall"
	"unicode/utf8"
)

// Kinds of a failed model request, as reported in ErrorDetail.Kind.
const (
	FailureInvalidAPIKey  = "invalid_api_key"
	FailureNotFound       = "not_found"
	FailureRateLimited    = "rate_limited"
	FailureContextLimit   = "context_limit"
	FailureBadRequest     = "bad_request"
	FailureServerError    = "server_error"
	FailureUnreachable    = "unreachable"
	FailureConnectionLost = "connection_lost"
	FailureTimeout        = "timeout"
	FailureProviderError  = "provider_error"
	FailureUnknown        = "unknown"
)

// ErrorDetail classifies the failure that ended a reply, next to its raw text.
type ErrorDetail struct {
	// Kind is one of the Failure* constants.
	Kind string `json:"kind"`
	// UpstreamStatus is the provider's HTTP status when it answered with one.
	UpstreamStatus int `json:"upstream_status,omitempty"`
	// ProviderMessage is the provider's own short error message with
	// credentials removed, when one could be read.
	ProviderMessage string `json:"provider_message,omitempty"`
}

// ProviderStreamError is an error the provider reported inside a stream
// rather than as an HTTP status.
type ProviderStreamError struct {
	Code    string
	Message string
}

func (e *ProviderStreamError) Error() string {
	detail := e.Message
	if e.Code != "" {
		detail = e.Code + ": " + detail
	}
	return "API stream error: " + detail
}

const maxProviderMessageRunes = 200

// secretLike matches words that look like credentials, so a provider that
// echoes part of a key in its error text does not leak it to the client.
var secretLike = regexp.MustCompile(`(?i)\b(?:sk|pk|rk|ak|key|token|bearer)[-_][A-Za-z0-9_\-]{8,}|[A-Za-z0-9_\-]{32,}`)

// ClassifyError describes err for clients. secrets are literal values (API
// keys, header values) that must never appear in the provider message.
func ClassifyError(err error, secrets ...string) ErrorDetail {
	if err == nil {
		return ErrorDetail{Kind: FailureUnknown}
	}
	if he, ok := errors.AsType[*HTTPError](err); ok {
		out := ErrorDetail{Kind: httpFailureKind(he.StatusCode), UpstreamStatus: he.StatusCode, ProviderMessage: ProviderMessage(he.Body, secrets...)}
		if IsContextLimit(err) {
			out.Kind = FailureContextLimit
		}
		return out
	}
	if se, ok := errors.AsType[*ProviderStreamError](err); ok {
		return ErrorDetail{Kind: streamFailureKind(se.Code, se.Message), ProviderMessage: RedactProviderMessage(se.Message, secrets...)}
	}
	if IsContextLimit(err) {
		return ErrorDetail{Kind: FailureContextLimit}
	}
	if _, ok := StreamStall(err); ok {
		return ErrorDetail{Kind: FailureTimeout}
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNABORTED) {
		return ErrorDetail{Kind: FailureConnectionLost}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrorDetail{Kind: FailureTimeout}
	}
	if ne, ok := errors.AsType[net.Error](err); ok {
		if ne.Timeout() {
			return ErrorDetail{Kind: FailureTimeout}
		}
		return ErrorDetail{Kind: FailureUnreachable}
	}
	if _, ok := errors.AsType[*url.Error](err); ok {
		return ErrorDetail{Kind: FailureUnreachable}
	}
	return ErrorDetail{Kind: FailureUnknown}
}

func httpFailureKind(status int) string {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return FailureInvalidAPIKey
	case status == http.StatusNotFound:
		return FailureNotFound
	case status == http.StatusTooManyRequests:
		return FailureRateLimited
	case status == http.StatusRequestTimeout || status == http.StatusGatewayTimeout:
		return FailureTimeout
	case status >= 500:
		return FailureServerError
	case status >= 400:
		return FailureBadRequest
	}
	return FailureUnknown
}

func streamFailureKind(code, message string) string {
	text := strings.ToLower(code + " " + message)
	for _, m := range contextLimitMarkers {
		if strings.Contains(text, m) {
			return FailureContextLimit
		}
	}
	code = strings.ToLower(code)
	switch {
	case strings.Contains(code, "rate_limit") || strings.Contains(code, "quota"):
		return FailureRateLimited
	case strings.Contains(code, "auth") || strings.Contains(code, "api_key") || strings.Contains(code, "permission"):
		return FailureInvalidAPIKey
	case strings.Contains(code, "not_found"):
		return FailureNotFound
	case strings.Contains(code, "overloaded") || strings.Contains(code, "server") || strings.Contains(code, "busy") || strings.Contains(code, "internal") || strings.Contains(code, "unavailable"):
		return FailureServerError
	case strings.Contains(code, "invalid_request"):
		return FailureBadRequest
	}
	return FailureProviderError
}

// ProviderMessage is the provider's own error message from a JSON error body,
// shortened and with credentials removed. Non-JSON bodies (HTML pages,
// proxies) give no message.
func ProviderMessage(body string, secrets ...string) string {
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
	return RedactProviderMessage(message, secrets...)
}

// RedactProviderMessage collapses whitespace, removes the literal secrets and
// anything that looks like a credential, and shortens message to at most 200
// characters.
func RedactProviderMessage(message string, secrets ...string) string {
	message = strings.Join(strings.Fields(message), " ")
	for _, secret := range secrets {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	message = secretLike.ReplaceAllString(message, "[redacted]")
	if utf8.RuneCountInString(message) > maxProviderMessageRunes {
		message = string([]rune(message)[:maxProviderMessageRunes-1]) + "…"
	}
	return message
}
