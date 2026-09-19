package privacy

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var enabled atomic.Bool

const maxInspectableBody = 32 << 20

const (
	tokenTTL            = 15 * time.Minute
	maxRememberedTokens = 4096
)

type rule struct {
	name       string
	rx         *regexp.Regexp
	valueGroup int
	check      func(string) bool
}

var rules = []rule{
	{name: "PRIVATE_KEY", rx: regexp.MustCompile(`-----BEGIN [^-]*PRIVATE KEY-----[\s\S]*?-----END [^-]*PRIVATE KEY-----`)},
	{name: "API_KEY", rx: regexp.MustCompile(`\b(?:sk-[A-Za-z0-9_-]{16,}|[sr]k_(?:live|test)_[A-Za-z0-9]{16,}|gh[pousr]_[A-Za-z0-9_]{20,}|github_pat_[A-Za-z0-9_]{30,}|AIza[A-Za-z0-9_-]{30,}|xox[baprs]-[A-Za-z0-9-]{10,}|cli_[a-z0-9]{16,}|ding[a-z0-9]{6,})\b`)},
	{name: "API_KEY", rx: regexp.MustCompile(`\b(?:sk-ant-[A-Za-z0-9_-]{20,}|hf_[A-Za-z0-9]{20,}|npm_[A-Za-z0-9]{30,}|pypi-[A-Za-z0-9_-]{20,})\b`)},
	{name: "ACCESS_KEY", rx: regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`)},
	{name: "ACCESS_KEY", rx: regexp.MustCompile(`\b(?:LTAI[A-Za-z0-9]{12,20}|AKID[A-Za-z0-9]{13,32})\b`)},
	{name: "ACCESS_KEY", rx: regexp.MustCompile(`(?i)aws[_-]?secret[_-]?access[_-]?key["']?\s*[:=]\s*["']?([A-Za-z0-9/+=]{40})(?:[^A-Za-z0-9/+=]|$)`), valueGroup: 1},
	{name: "TOKEN", rx: regexp.MustCompile(`(?i)\bBearer\s+([A-Za-z0-9._~+/=-]{20,})`), valueGroup: 1},
	{name: "JWT", rx: regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`), check: validJWT},
	{name: "CONNSTR", rx: regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]{0,63}://[^\s:@/]+:([^\s@/]{4,})@`), valueGroup: 1},
	{name: "EMAIL", rx: regexp.MustCompile(`(?i)(?:[A-Z0-9._%+\-\x{4e00}-\x{9fff}]+)@(?:[A-Z0-9\-\x{4e00}-\x{9fff}]+\.)+[A-Z\x{4e00}-\x{9fff}]{2,}`)},
	{name: "PHONE", rx: regexp.MustCompile(`(?:\+?86[\s-]?)?1[3-9][0-9](?:[\s-]?[0-9]{4}){2}`)},
	{name: "IP", rx: regexp.MustCompile(`\b(?:(?:25[0-5]|2[0-4]\d|1?\d?\d)\.){3}(?:25[0-5]|2[0-4]\d|1?\d?\d)\b`), check: validIPv4},
	{name: "IPV6", rx: regexp.MustCompile(`(?i)[0-9a-f:]{2,45}`), check: validIPv6},
	{name: "IDCARD", rx: regexp.MustCompile(`\b[1-9][0-9]{5}(?:19|20)[0-9]{2}(?:0[1-9]|1[0-2])(?:0[1-9]|[12][0-9]|3[01])[0-9]{3}[0-9Xx]\b`), check: validIDCard},
	{name: "LANDLINE", rx: regexp.MustCompile(`(?:\+?86[\s-]?)?(?:\(0\d{2,3}\)|0\d{2,3})[\s-]?[2-9]\d{6,7}(?:[\s-]?(?:转|分机|ext\.?|x|#)[\s-]?\d{1,5})?`)},
	{name: "CARD", rx: regexp.MustCompile(`\b[3-6][0-9]{12,18}\b`), check: validCardNumber},
	{name: "IBAN", rx: regexp.MustCompile(`\b[A-Z]{2}[0-9]{2}[A-Z0-9]{11,30}\b`), check: validIBAN},
	{name: "MAC", rx: regexp.MustCompile(`(?i)\b[0-9a-f]{2}(?::[0-9a-f]{2}){5}\b`)},
	{name: "SECRET", rx: regexp.MustCompile(`(?i)(?:api[_-]?key|access[_-]?token|auth[_-]?token|password|passwd|pwd|secret|token|private[_-]?key|密码|口令|令牌|密钥|秘钥|凭据|凭证|私钥)["'“”「」]?\s*[:=：＝]\s*["'“”「」]?([A-Za-z0-9!@#$%^&*_~+=-]{6,64})`), valueGroup: 1, check: validSecret},
}

var (
	tokenMu       sync.Mutex
	remembered    = make(map[string]rememberedToken)
	rememberedKey = make(map[string]string)
	tokenCounter  atomic.Uint64
)

type rememberedToken struct {
	value   string
	key     string
	expires time.Time
}

func Enabled() bool { return enabled.Load() }

func SetEnabled(on bool) {
	enabled.Store(on)
	if !on {
		ClearMemory()
	}
}

func ClearMemory() {
	tokenMu.Lock()
	remembered = make(map[string]rememberedToken)
	rememberedKey = make(map[string]string)
	tokenMu.Unlock()
}

func Toggle() bool {
	for {
		old := enabled.Load()
		if enabled.CompareAndSwap(old, !old) {
			if old {
				ClearMemory()
			}
			return !old
		}
	}
}

func MaskText(text string) (string, map[string]string) {
	if text == "" {
		return text, nil
	}
	values := make(map[string]string)
	byValue := make(map[string]string)
	mask := func(label, value string) string {
		if value == "" || strings.Contains(value, "__KBR_") {
			return value
		}
		key := label + "\x00" + value
		if token, ok := byValue[key]; ok {
			return token
		}
		if token, ok := rememberedTokenFor(key); ok {
			byValue[key] = token
			values[token] = value
			return token
		}
		n := tokenCounter.Add(1)
		token := "__KBR_" + label + "_" + formatSeq(int(n)) + "__"
		byValue[key] = token
		values[token] = value
		rememberToken(token, key, value)
		return token
	}
	for _, r := range rules {
		text = r.rx.ReplaceAllStringFunc(text, func(match string) string {
			if strings.Contains(match, "__KBR_") {
				return match
			}
			if r.check != nil && !r.check(match) {
				return match
			}
			if r.valueGroup > 0 {
				parts := r.rx.FindStringSubmatchIndex(match)
				group := 2 * r.valueGroup
				if len(parts) > group+1 && parts[group] >= 0 {
					value := match[parts[group]:parts[group+1]]
					return match[:parts[group]] + mask(r.name, value) + match[parts[group+1]:]
				}
			}
			return mask(r.name, match)
		})
	}
	if len(values) == 0 {
		return text, nil
	}
	return text, values
}

func rememberToken(token, key, value string) {
	now := time.Now()
	tokenMu.Lock()
	defer tokenMu.Unlock()
	for t, item := range remembered {
		if !item.expires.After(now) {
			delete(remembered, t)
			if rememberedKey[item.key] == t {
				delete(rememberedKey, item.key)
			}
		}
	}
	if old, ok := rememberedKey[key]; ok {
		delete(remembered, old)
	}
	for len(remembered) >= maxRememberedTokens {
		var oldest string
		var ts time.Time
		for t, item := range remembered {
			if oldest == "" || item.expires.Before(ts) {
				oldest, ts = t, item.expires
			}
		}
		if oldest == "" {
			break
		}
		item := remembered[oldest]
		delete(remembered, oldest)
		if rememberedKey[item.key] == oldest {
			delete(rememberedKey, item.key)
		}
	}
	remembered[token] = rememberedToken{value: value, key: key, expires: now.Add(tokenTTL)}
	rememberedKey[key] = token
}

func rememberedTokenFor(key string) (string, bool) {
	now := time.Now()
	tokenMu.Lock()
	defer tokenMu.Unlock()
	token, ok := rememberedKey[key]
	if !ok {
		return "", false
	}
	item, ok := remembered[token]
	if !ok || !item.expires.After(now) {
		delete(rememberedKey, key)
		delete(remembered, token)
		return "", false
	}
	return token, true
}

func rememberedValues() map[string]string {
	now := time.Now()
	out := make(map[string]string)
	tokenMu.Lock()
	defer tokenMu.Unlock()
	for token, item := range remembered {
		if !item.expires.After(now) {
			delete(remembered, token)
			if rememberedKey[item.key] == token {
				delete(rememberedKey, item.key)
			}
			continue
		}
		out[token] = item.value
	}
	return out
}

func validJWT(value string) bool {
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return false
	}
	for i, part := range parts {
		if part == "" {
			return false
		}
		decoded, err := base64.RawURLEncoding.DecodeString(part)
		if err != nil {
			return false
		}
		if i < 2 {
			var obj map[string]any
			if json.Unmarshal(decoded, &obj) != nil {
				return false
			}
		}
	}
	return true
}

func validCardNumber(value string) bool {
	if len(value) < 13 || len(value) > 19 {
		return false
	}
	return luhn(value)
}

func validIBAN(value string) bool {
	if len(value) < 15 || len(value) > 34 {
		return false
	}
	value = strings.ToUpper(value)
	if value[0] < 'A' || value[0] > 'Z' || value[1] < 'A' || value[1] > 'Z' {
		return false
	}
	for i := 2; i < len(value); i++ {
		if !((value[i] >= 'A' && value[i] <= 'Z') || (value[i] >= '0' && value[i] <= '9')) {
			return false
		}
	}
	rotated := value[4:] + value[:4]
	mod := 0
	for i := 0; i < len(rotated); i++ {
		c := rotated[i]
		if c >= 'A' && c <= 'Z' {
			for _, digit := range strconv.Itoa(int(c-'A') + 10) {
				mod = (mod*10 + int(digit-'0')) % 97
			}
		} else {
			mod = (mod*10 + int(c-'0')) % 97
		}
	}
	return mod == 1
}

func luhn(value string) bool {
	sum := 0
	double := false
	for i := len(value) - 1; i >= 0; i-- {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
		d := int(value[i] - '0')
		if double {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		double = !double
	}
	return sum%10 == 0
}

func validIPv6(value string) bool {
	if strings.Count(value, ":") < 2 {
		return false
	}
	return net.ParseIP(value) != nil
}

func validIPv4(value string) bool {
	return net.ParseIP(value) != nil && strings.Count(value, ".") == 3
}

func validIDCard(value string) bool {
	if len(value) != 18 {
		return false
	}
	if _, err := time.Parse("20060102", value[6:14]); err != nil {
		return false
	}
	weights := [...]int{7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2}
	checks := "10X98765432"
	sum := 0
	for i, weight := range weights {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
		sum += int(value[i]-'0') * weight
	}
	check := value[17]
	if check == 'x' {
		check = 'X'
	}
	return check == checks[sum%11]
}

func validSecret(value string) bool {
	if len(value) < 6 || strings.Contains(value, "__KBR_") {
		return false
	}
	for _, r := range value {
		if (r >= '0' && r <= '9') || strings.ContainsRune("!@#$%^&*", r) {
			return true
		}
	}
	return false
}

func formatSeq(n int) string {
	s := strconv.Itoa(n)
	for len(s) < 4 {
		s = "0" + s
	}
	return s
}

type Transport struct{ Base http.RoundTripper }

func WrapTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	if _, ok := base.(*Transport); ok {
		return base
	}
	return &Transport{Base: base}
}

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !Enabled() || req.Body == nil || req.Method == http.MethodGet || req.Method == http.MethodHead || !requestTextualContentType(req.Header.Get("Content-Type")) || req.Header.Get("Content-Encoding") != "" {
		return t.Base.RoundTrip(req)
	}
	originalBody := req.Body
	raw, err := io.ReadAll(io.LimitReader(originalBody, maxInspectableBody+1))
	if err != nil {
		_ = originalBody.Close()
		return nil, err
	}
	if len(raw) > maxInspectableBody {
		req.Body = io.NopCloser(io.MultiReader(bytes.NewReader(raw), originalBody))
		return t.Base.RoundTrip(req)
	}
	_ = originalBody.Close()
	masked, values := MaskText(string(raw))
	if len(values) == 0 {
		restoreRequestBody(req, raw)
		return t.roundTripResponse(req, nil)
	}
	body := []byte(masked)
	maskedReq := req.Clone(req.Context())
	maskedReq.Body = io.NopCloser(bytes.NewReader(body))
	maskedReq.ContentLength = int64(len(body))
	maskedReq.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	return t.roundTripResponse(maskedReq, values)
}

func (t *Transport) roundTripResponse(req *http.Request, values map[string]string) (*http.Response, error) {
	resp, err := t.Base.RoundTrip(req)
	if err != nil || resp == nil || resp.Body == nil {
		return resp, err
	}
	if !responseTextualContentType(resp.Header.Get("Content-Type")) || resp.Header.Get("Content-Encoding") != "" {
		return resp, nil
	}
	if values == nil {
		values = make(map[string]string)
	}
	for token, value := range rememberedValues() {
		if _, exists := values[token]; !exists {
			values[token] = value
		}
	}
	resp.ContentLength = -1
	resp.Header.Del("Content-Length")
	resp.Body = &restoreReader{src: resp.Body, values: values, maxToken: maxTokenLength(values)}
	return resp, nil
}

func requestTextualContentType(value string) bool {
	if strings.TrimSpace(value) == "" {
		return true
	}
	return textualContentType(value)
}

func responseTextualContentType(value string) bool {
	if strings.TrimSpace(value) == "" {
		return false
	}
	return textualContentType(value)
}

func textualContentType(value string) bool {
	value = strings.TrimSpace(value)
	media, _, err := mime.ParseMediaType(value)
	if err != nil {
		return false
	}
	if media == "application/json" || media == "application/ndjson" || media == "application/x-ndjson" || media == "application/sse" || media == "application/x-www-form-urlencoded" || media == "application/graphql" || media == "application/xml" || media == "application/soap+xml" {
		return true
	}
	return strings.HasPrefix(media, "text/") || strings.HasSuffix(media, "+json") || strings.HasSuffix(media, "+text")
}

func restoreRequestBody(req *http.Request, body []byte) {
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
}

func maxTokenLength(values map[string]string) int {
	n := 0
	for token := range values {
		if len(token) > n {
			n = len(token)
		}
	}
	return n
}

type restoreReader struct {
	src      io.ReadCloser
	values   map[string]string
	ordered  []string
	maxToken int
	pending  []byte
	ready    []byte
	done     bool
	err      error
	buf      []byte
}

func (r *restoreReader) Read(dst []byte) (int, error) {
	if len(r.ordered) == 0 {
		r.ordered = make([]string, 0, len(r.values))
		for token := range r.values {
			r.ordered = append(r.ordered, token)
		}
		sort.Slice(r.ordered, func(i, j int) bool { return len(r.ordered[i]) > len(r.ordered[j]) })
	}
	if len(r.ready) > 0 {
		return r.emit(dst)
	}
	for {
		if r.done && len(r.pending) == 0 {
			if r.err != nil {
				err := r.err
				r.err = nil
				return 0, err
			}
			return 0, io.EOF
		}
		if !r.done {
			if len(r.buf) == 0 {
				r.buf = make([]byte, 32*1024)
			}
			n, err := r.src.Read(r.buf)
			if n > 0 {
				r.pending = append(r.pending, r.buf[:n]...)
			}
			if err == io.EOF {
				r.done = true
			} else if err != nil {
				r.done = true
				r.err = err
			}
		}
		keep := 0
		if !r.done && r.maxToken > 1 {
			keep = r.maxToken - 1
			if keep > len(r.pending) {
				keep = len(r.pending)
			}
		}
		if len(r.pending) <= keep {
			continue
		}
		cut := len(r.pending) - keep
		if !r.done {
			cut = r.safeCut(cut)
			if cut == 0 {
				continue
			}
		}
		out := string(r.pending[:cut])
		for _, token := range r.ordered {
			out = strings.ReplaceAll(out, token, r.values[token])
		}
		r.pending = append([]byte(nil), r.pending[cut:]...)
		r.ready = []byte(out)
		if len(r.ready) > 0 {
			return r.emit(dst)
		}
	}
}

func (r *restoreReader) safeCut(cut int) int {
	for _, token := range r.ordered {
		start := cut - len(token) + 1
		if start < 0 {
			start = 0
		}
		for i := start; i < cut; i++ {
			part := r.pending[i:cut]
			if len(part) < len(token) && strings.HasPrefix(token, string(part)) {
				if i < cut {
					cut = i
				}
				break
			}
		}
	}
	return cut
}

func (r *restoreReader) emit(dst []byte) (int, error) {
	n := len(r.ready)
	if n > len(dst) {
		n = len(dst)
	}
	copy(dst, r.ready[:n])
	r.ready = r.ready[n:]
	return n, nil
}

func (r *restoreReader) Close() error { return r.src.Close() }
