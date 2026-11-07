package elicit

import (
	"fmt"
	"math"
	"net/mail"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

var patternMu sync.Mutex

// Errors maps a field name to what is wrong with its value. The key "" holds
// problems that belong to no field, such as an unknown property.
type Errors map[string]string

func (e Errors) Error() string {
	keys := make([]string, 0, len(e))
	for k := range e {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		if k == "" {
			parts[i] = e[k]
		} else {
			parts[i] = fmt.Sprintf("%s: %s", k, e[k])
		}
	}
	return strings.Join(parts, "; ")
}

// Validate checks content, an accepted answer, against fields and returns it
// in canonical JSON form (string, float64, bool, []any of strings), ready to
// send to the server. Every key must name a field; required fields must be
// present and not null; each value must match its field's type and
// constraints. A null value of an optional field counts as absent.
func Validate(fields []ai.ElicitField, content map[string]any) (map[string]any, Errors) {
	errs := Errors{}
	out := make(map[string]any, len(content))
	byName := make(map[string]ai.ElicitField, len(fields))
	for _, f := range fields {
		byName[f.Name] = f
	}
	var unknown []string
	for k := range content {
		if _, ok := byName[k]; !ok {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		errs[""] = fmt.Sprintf("%q is not a field of this form", unknown[0])
	}
	for _, f := range fields {
		v, present := content[f.Name]
		if !present || v == nil {
			if f.Required {
				errs[f.Name] = "is required"
			}
			continue
		}
		norm, msg := checkValue(f, v)
		if msg != "" {
			errs[f.Name] = msg
			continue
		}
		out[f.Name] = norm
	}
	if len(errs) > 0 {
		return nil, errs
	}
	return out, nil
}

// CheckField reports what is wrong with v as the value of f ("" when valid).
// Clients use it to show a message beside a field as the user types.
func CheckField(f ai.ElicitField, v any) string {
	if v == nil {
		if f.Required {
			return "is required"
		}
		return ""
	}
	_, msg := checkValue(f, v)
	return msg
}

func checkValue(f ai.ElicitField, v any) (any, string) {
	switch f.Type {
	case "string":
		s, ok := v.(string)
		if !ok {
			return nil, "must be a string"
		}
		if len(f.Options) > 0 {
			if !hasOption(f, s) {
				return nil, "must be one of the listed choices"
			}
			return s, ""
		}
		n := utf8.RuneCountInString(s)
		if f.MinLength != nil && n < *f.MinLength {
			return nil, fmt.Sprintf("must be at least %d characters", *f.MinLength)
		}
		if f.MaxLength != nil && n > *f.MaxLength {
			return nil, fmt.Sprintf("must be at most %d characters", *f.MaxLength)
		}
		if msg := checkFormat(f.Format, s); msg != "" {
			return nil, msg
		}
		if f.Pattern != "" {
			re, err := compilePattern(f.Pattern)
			if err != nil || !re.MatchString(s) {
				return nil, "does not match the required pattern"
			}
		}
		return s, ""
	case "number", "integer":
		x, ok := toFloat(v)
		if !ok {
			return nil, "must be a number"
		}
		if f.Type == "integer" && x != math.Trunc(x) {
			return nil, "must be a whole number"
		}
		if f.Minimum != nil && x < *f.Minimum {
			return nil, fmt.Sprintf("must be at least %s", formatNumber(*f.Minimum))
		}
		if f.Maximum != nil && x > *f.Maximum {
			return nil, fmt.Sprintf("must be at most %s", formatNumber(*f.Maximum))
		}
		return x, ""
	case "boolean":
		b, ok := v.(bool)
		if !ok {
			return nil, "must be true or false"
		}
		return b, ""
	case "array":
		var items []string
		switch list := v.(type) {
		case []any:
			for _, item := range list {
				s, ok := item.(string)
				if !ok {
					return nil, "must be a list of choices"
				}
				items = append(items, s)
			}
		case []string:
			items = list
		default:
			return nil, "must be a list of choices"
		}
		seen := map[string]bool{}
		for _, s := range items {
			if !hasOption(f, s) {
				return nil, fmt.Sprintf("%q is not one of the listed choices", s)
			}
			if seen[s] {
				return nil, fmt.Sprintf("%q is chosen twice", s)
			}
			seen[s] = true
		}
		if f.MinItems != nil && len(items) < *f.MinItems {
			return nil, fmt.Sprintf("choose at least %d", *f.MinItems)
		}
		if f.MaxItems != nil && len(items) > *f.MaxItems {
			return nil, fmt.Sprintf("choose at most %d", *f.MaxItems)
		}
		// Kept in option order, so the same choice always reads the same.
		out := make([]any, 0, len(items))
		for _, o := range f.Options {
			if seen[o.Value] {
				out = append(out, o.Value)
			}
		}
		return out, ""
	}
	return nil, "has an unsupported type"
}

func hasOption(f ai.ElicitField, v string) bool {
	return slices.ContainsFunc(f.Options, func(o ai.ElicitOption) bool { return o.Value == v })
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, !math.IsNaN(n) && !math.IsInf(n, 0)
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case interface{ Float64() (float64, error) }:
		x, err := n.Float64()
		return x, err == nil
	}
	return 0, false
}

func formatNumber(x float64) string {
	return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%f", x), "0"), ".")
}

func checkFormat(format, s string) string {
	switch format {
	case "email":
		addr, err := mail.ParseAddress(s)
		if err != nil || addr.Name != "" || addr.Address != s || !strings.Contains(s, "@") {
			return "must be an email address"
		}
	case "uri":
		u, err := url.Parse(s)
		if err != nil || u.Scheme == "" || strings.ContainsAny(s, " \t\n") {
			return "must be an absolute URI, such as https://example.com"
		}
	case "date":
		if _, err := time.Parse(time.DateOnly, s); err != nil {
			return "must be a date (YYYY-MM-DD)"
		}
	case "date-time":
		if _, err := time.Parse(time.RFC3339, s); err != nil {
			return "must be a date and time (RFC 3339, such as 2026-01-02T15:04:05Z)"
		}
	}
	return ""
}

// Redact returns content without the values of sensitive fields, and the
// names of the fields it left out. The journal and history keep the result;
// only the MCP server receives the full answer.
func Redact(fields []ai.ElicitField, content map[string]any) (map[string]any, []string) {
	if content == nil {
		return nil, nil
	}
	out := make(map[string]any, len(content))
	var redacted []string
	for k, v := range content {
		out[k] = v
	}
	for _, f := range fields {
		if _, ok := out[f.Name]; ok && f.Sensitive {
			delete(out, f.Name)
			redacted = append(redacted, f.Name)
		}
	}
	return out, redacted
}

// sensitiveWords are whole words of a field's name or title that read like a
// secret; sensitiveJoined are matched anywhere in the name and title with
// separators removed ("api_key", "apiKey" and "API key" all read "apikey").
var (
	sensitiveWords  = []string{"token", "pin", "otp", "totp", "mfa", "cvv", "cvc", "ssn", "pwd", "secret", "password"}
	sensitiveJoined = []string{"password", "passwd", "passphrase", "secret", "apikey", "accesskey", "privatekey", "secretkey", "credential", "authtoken", "accesstoken", "refreshtoken", "bearer", "cardnumber", "creditcard", "securitycode", "onetimecode", "verificationcode"}
)

// looksSensitive reports whether a free-text field named name with title
// asks for what reads like a secret. The specification forbids servers from
// requesting secrets through forms; this is a guard for when one does.
func looksSensitive(name, title string) bool {
	words := splitWords(name + " " + title)
	joined := strings.Join(words, "")
	for _, w := range words {
		if slices.Contains(sensitiveWords, w) {
			return true
		}
	}
	for _, s := range sensitiveJoined {
		if strings.Contains(joined, s) {
			return true
		}
	}
	return false
}

// splitWords splits s into lower-case words at non-alphanumerics and at
// camelCase boundaries.
func splitWords(s string) []string {
	var words []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			words = append(words, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	runes := []rune(s)
	for i, r := range runes {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			cur = append(cur, r)
		case r >= 'A' && r <= 'Z':
			if len(cur) > 0 && (cur[len(cur)-1] < 'A' || cur[len(cur)-1] > 'Z' || (i+1 < len(runes) && runes[i+1] >= 'a' && runes[i+1] <= 'z')) {
				flush()
			}
			cur = append(cur, r)
		case r > 127 && (r != ' '):
			cur = append(cur, r)
		default:
			flush()
		}
	}
	flush()
	return words
}
