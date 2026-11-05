package imaging

import (
	"context"
	"errors"
	"net"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxReasonRunes bounds a public failure reason so it stays a one-line hint, not a body dump.
const maxReasonRunes = 160

var (
	reasonURL      = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://\S+`)
	reasonBearer   = regexp.MustCompile(`(?i)\b(?:bearer|basic)\s+\S+`)
	reasonToken    = regexp.MustCompile(`\b(?:sk|key|tok)[-_][A-Za-z0-9_-]{6,}\b|\b[A-Za-z0-9_-]{32,}\b`)
	reasonEmail    = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	reasonAddr     = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}(?::\d+)?\b|\[[0-9a-fA-F:]+\](?::\d+)?`)
	reasonPath     = regexp.MustCompile(`(?:[A-Za-z]:\\|/)[^\s"']+[\\/][^\s"']+`)
	reasonSpace    = regexp.MustCompile(`\s+`)
	reasonKeyValue = regexp.MustCompile(`(?i)\b[\w-]*(?:key|token|secret|password|passwd|signature|credential|sig)[\w-]*\s*[=:]\s*\S+`)
)

// SafeReason is the last line of defence for reasons on the public wire: one short line
// without URLs, credentials, addresses, e-mail addresses or file paths. A blocklist cannot
// clean arbitrary text, so callers pass fixed phrases and enums, never provider prose.
func SafeReason(s string) string {
	if s == "" {
		return ""
	}
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "")
	}
	s = strings.Map(func(r rune) rune {
		// Cf covers bidi overrides and zero-width characters. Dropping them, not spacing
		// them out, keeps a disguised secret in one piece for the patterns below.
		if unicode.Is(unicode.Cf, r) {
			return -1
		}
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	for _, re := range []*regexp.Regexp{reasonURL, reasonBearer, reasonEmail, reasonPath, reasonAddr, reasonKeyValue, reasonToken} {
		s = re.ReplaceAllString(s, "[redacted]")
	}
	s = strings.TrimSpace(reasonSpace.ReplaceAllString(s, " "))
	if utf8.RuneCountInString(s) > maxReasonRunes {
		s = string([]rune(s)[:maxReasonRunes-1]) + "…"
	}
	return s
}

// TransportReason describes a transport failure with fixed phrases only, so an
// error that embeds a request URL or header can never leak through it.
func TransportReason(err error) string {
	var dns *net.DNSError
	var netErr net.Error
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.DeadlineExceeded):
		return "request timed out"
	case errors.Is(err, context.Canceled):
		return "request was cancelled"
	case errors.As(err, &dns):
		return "provider host could not be resolved"
	case errors.As(err, &netErr) && netErr.Timeout():
		return "request timed out"
	case errors.As(err, &netErr):
		return "connection to the provider failed"
	default:
		return ""
	}
}
