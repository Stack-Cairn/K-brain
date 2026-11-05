package tui

// Error display: a failed turn becomes a short typed banner — what went wrong
// in a few words, the provider's own message, and what to do next — instead
// of the raw error string. The kind is decided from the typed error when one
// is at hand (an *ai.HTTPError carries the status code) and from the message
// text otherwise.

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

type errKind string

const (
	errAuth        errKind = "auth"
	errQuota       errKind = "quota"
	errRateLimit   errKind = "rate_limit"
	errContext     errKind = "context"
	errOverloaded  errKind = "overloaded"
	errServer      errKind = "server"
	errNetwork     errKind = "network"
	errModel       errKind = "model"
	errBadRequest  errKind = "bad_request"
	errOutputLimit errKind = "output_limit"
	errOther       errKind = "other"
)

// errInfo is a classified error: its kind, the HTTP status when one is known,
// and the provider's message with the transport noise taken off.
type errInfo struct {
	kind   errKind
	status int
	cause  string
}

// classifyError decides what kind of failure err is.
func classifyError(err error) errInfo {
	if err == nil {
		return errInfo{kind: errOther}
	}
	info := errInfo{cause: err.Error()}
	if he, ok := errors.AsType[*ai.HTTPError](err); ok {
		info.status = he.StatusCode
		if fields := strings.Fields(he.Status); info.status == 0 && len(fields) > 0 {
			info.status, _ = strconv.Atoi(fields[0])
		}
		info.cause = providerMessage(he.Body)
		if info.cause == "" {
			info.cause = strings.TrimSpace(he.Status)
		}
	}
	if _, ok := errors.AsType[*ai.OutputLimitError](err); ok {
		info.kind = errOutputLimit
		return info
	}
	if ai.IsContextLimit(err) {
		info.kind = errContext
		return info
	}
	lower := strings.ToLower(err.Error())
	var netErr net.Error
	switch {
	case info.status != 0:
		info.kind = kindForStatus(info.status, lower)
	case errors.As(err, &netErr), errors.Is(err, context.DeadlineExceeded):
		info.kind = errNetwork
	default:
		if info.status = statusInText(err.Error()); info.status != 0 {
			info.kind = kindForStatus(info.status, lower)
		} else {
			info.kind = kindForText(lower)
		}
	}
	return info
}

func kindForStatus(code int, lower string) errKind {
	switch {
	case code == 401 || code == 403:
		if strings.Contains(lower, "quota") || strings.Contains(lower, "billing") {
			return errQuota
		}
		return errAuth
	case code == 402:
		return errQuota
	case code == 429:
		if strings.Contains(lower, "insufficient_quota") || strings.Contains(lower, "billing") {
			return errQuota
		}
		return errRateLimit
	case code == 404:
		return errModel
	case code == 529 || strings.Contains(lower, "overloaded"):
		return errOverloaded
	case code >= 500 && code <= 599:
		return errServer
	case code == 400 || code == 413 || code == 422:
		return errBadRequest
	}
	return kindForText(lower)
}

// kindForText classifies an error from its message alone.
func kindForText(lower string) errKind {
	has := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(lower, w) {
				return true
			}
		}
		return false
	}
	switch {
	case has("invalid api key", "invalid_api_key", "unauthorized", "authentication", "api key", "permission denied", "forbidden"):
		return errAuth
	case has("insufficient_quota", "quota", "billing", "credit balance"):
		return errQuota
	case has("rate limit", "rate_limit", "too many requests"):
		return errRateLimit
	case has("overloaded"):
		return errOverloaded
	case has("connection refused", "no such host", "connection reset", "i/o timeout", "tls:", "network is unreachable",
		"broken pipe", "unexpected eof", "eof", "timeout", "dial tcp", "server misbehaving"):
		return errNetwork
	case has("model not found", "model_not_found", "does not exist", "unknown model"):
		return errModel
	}
	return errOther
}

// textStatuses are the HTTP statuses statusInText recognises. Only these
// count, so a port or a line number in the message is not read as one.
var textStatuses = map[int]bool{
	400: true, 401: true, 402: true, 403: true, 404: true, 408: true, 409: true, 413: true, 422: true, 429: true,
	500: true, 502: true, 503: true, 504: true, 529: true,
}

// statusInText finds a leading or embedded HTTP status in an error string,
// e.g. "429 Too Many Requests: …" or "provider returned 529: …".
func statusInText(s string) int {
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r < '0' || r > '9' }) {
		if len(f) != 3 {
			continue
		}
		if n, _ := strconv.Atoi(f); textStatuses[n] {
			return n
		}
	}
	return 0
}

// providerMessage pulls the human message out of a provider's error body,
// which is usually JSON ({"error":{"message":…}} or {"message":…}).
func providerMessage(body string) string {
	body = strings.TrimSpace(body)
	var v struct {
		Message string `json:"message"`
		Detail  string `json:"detail"`
		Error   json.RawMessage
	}
	if json.Unmarshal([]byte(body), &v) == nil {
		var inner struct {
			Message string `json:"message"`
		}
		var flat string
		switch {
		case len(v.Error) > 0 && json.Unmarshal(v.Error, &inner) == nil && inner.Message != "":
			return inner.Message
		case len(v.Error) > 0 && json.Unmarshal(v.Error, &flat) == nil && flat != "":
			return flat
		case v.Message != "":
			return v.Message
		case v.Detail != "":
			return v.Detail
		}
	}
	return body
}

// errTitle is the banner's title for a kind.
func (i errInfo) title() string {
	t := map[errKind]string{
		errAuth:        "Authentication failed",
		errQuota:       "Quota or billing limit reached",
		errRateLimit:   "Rate limited",
		errContext:     "Context window full",
		errOverloaded:  "Provider overloaded",
		errServer:      "Provider error",
		errNetwork:     "Network error",
		errModel:       "Model not available",
		errBadRequest:  "Request rejected",
		errOutputLimit: "Reply cut off at the output limit",
	}[i.kind]
	if t == "" {
		t = "Error"
	}
	if i.status != 0 {
		t += " · " + strconv.Itoa(i.status)
	}
	return t
}

// hint is the next step a kind suggests.
func (i errInfo) hint() string {
	return map[errKind]string{
		errAuth:        "Check the provider's API key in config.json, then send again.",
		errQuota:       "Check the account's plan or credits, or switch with /model.",
		errRateLimit:   "Wait a moment and send again, or switch with /model.",
		errContext:     "Run /compact or /new-context, then send again.",
		errOverloaded:  "Send again in a moment, or switch with /model.",
		errServer:      "Send again in a moment, or switch with /model.",
		errNetwork:     "Check the connection and the provider's base URL, then send again.",
		errModel:       "Pick another model with /model.",
		errBadRequest:  "The provider refused the request; /context shows what was sent.",
		errOutputLimit: "Ask to continue, or raise the model's output limit.",
	}[i.kind]
}

// retryReason is the short cause shown next to "Retrying" in the status row.
func retryReason(err error) string {
	if err == nil {
		return ""
	}
	i := classifyError(err)
	r := map[errKind]string{
		errRateLimit:  "rate limited",
		errOverloaded: "overloaded",
		errServer:     "server error",
		errNetwork:    "network",
		errAuth:       "auth",
		errQuota:      "quota",
	}[i.kind]
	switch {
	case r != "" && i.status != 0:
		return r + " " + strconv.Itoa(i.status)
	case r != "":
		return r
	case i.status != 0:
		return "HTTP " + strconv.Itoa(i.status)
	}
	return truncLine(oneLineText(i.cause), 40)
}

// errBannerPrefix tags a transcript row as a typed error banner: the stored
// text is "error[<kind>:<status>]: <cause>", so a copy of the transcript still
// reads as an error line.
const errBannerPrefix = "error["

// appendTurnError adds the banner for a failed turn.
func (m *model) appendTurnError(err error) {
	m.status().lastTurnFailed = true
	m.appendRaw(blockText, errBannerText(classifyError(err)))
}

func errBannerText(i errInfo) string {
	return errBannerPrefix + string(i.kind) + ":" + strconv.Itoa(i.status) + "]: " + oneLineText(i.cause)
}

// parseErrBanner reads a banner row back into its parts.
func parseErrBanner(text string) (errInfo, bool) {
	rest, ok := strings.CutPrefix(text, errBannerPrefix)
	if !ok {
		return errInfo{}, false
	}
	tag, cause, ok := strings.Cut(rest, "]: ")
	if !ok {
		return errInfo{}, false
	}
	kind, code, _ := strings.Cut(tag, ":")
	status, _ := strconv.Atoi(code)
	return errInfo{kind: errKind(kind), status: status, cause: cause}, true
}

// renderErrorBanner draws a banner row: an error rail, a bold title, the
// provider's message (at most three lines) and the hint.
func renderErrorBanner(text string, width int) (string, bool) {
	info, ok := parseErrBanner(text)
	if !ok {
		return "", false
	}
	rail := errorTitleStyle.Render(glyphs.Band) + " "
	inner := max(width-2, 1)
	out := []string{rail + errorTitleStyle.Render(ansi.Truncate(chromeGlyphs(info.title()), inner, chromeGlyphs("…")))}
	if info.cause != "" {
		lines := strings.Split(wrap(chromeGlyphs(info.cause), inner), "\n")
		if len(lines) > 3 {
			lines = append(lines[:2], ansi.Truncate(lines[2], max(inner-1, 1), "")+chromeGlyphs("…"))
		}
		for _, l := range lines {
			out = append(out, rail+textSecondaryStyle.Render(l))
		}
	}
	if h := info.hint(); h != "" {
		for l := range strings.SplitSeq(wrap(chromeGlyphs(h), inner), "\n") {
			out = append(out, rail+textHintStyle.Render(l))
		}
	}
	return strings.Join(out, "\n"), true
}

// oneLineText folds whitespace runs, newlines included, into single spaces.
func oneLineText(s string) string {
	return strings.Join(strings.Fields(ansi.Strip(s)), " ")
}
