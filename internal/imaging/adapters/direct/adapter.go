package direct

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

const (
	OpenAIID     = "openai-images"
	GoogleID     = "google-generate-content-image"
	VolcengineID = "volcengine-ark-image"
	Version      = "1"
)

// Options are backend-only. The adapter owns the authentication header: it
// resolves SecretRef in Scope for each submit, never from environment variables.
// HTTPClient must have a trusted transport that does not retry, inject auth,
// log sensitive payloads, or redirect requests itself. Network/proxy policy is
// owned by the caller. A client copy disables redirects and its cookie jar.
// BaseURL includes the API prefix (/v1, /v1beta, /api/v3 respectively).
// Plain HTTP is accepted only for loopback httptest servers.
type Options struct {
	HTTPClient     *http.Client
	BaseURL        string
	SecretResolver imaging.SecretResolver
	Scope          imaging.Scope
	SecretRef      string
	// CustomHeaders is copied and validated at construction. Only restricted
	// X-* extension headers and OpenAI-Organization/OpenAI-Project are allowed.
	// Authentication, HTTP controls, forwarding and idempotency are reserved.
	CustomHeaders map[string]string
	Limits        Limits
	Profile       string
}

// Limits may tighten, but never enlarge, the local hard ceilings. Zero uses
// defaults. Google additionally limits the entire encoded request to 20 MB.
type Limits struct {
	MaxRequestBytes  int64
	MaxResponseBytes int64
	MaxImageBytes    int64
	MaxPixels        int64
}

type Adapter struct {
	officialOpenAI bool
	id             string
	profile        string
	client         http.Client
	base           string
	secrets        imaging.SecretResolver
	scope          imaging.Scope
	secretRef      string
	headers        http.Header
	limits         Limits
}

var _ imaging.Adapter = (*Adapter)(nil)

func NewOpenAI(o Options) (*Adapter, error)     { return newAdapter(OpenAIID, o) }
func NewGoogle(o Options) (*Adapter, error)     { return newAdapter(GoogleID, o) }
func NewVolcengine(o Options) (*Adapter, error) { return newAdapter(VolcengineID, o) }

func newAdapter(id string, o Options) (*Adapter, error) {
	u, err := url.Parse(o.BaseURL)
	if err != nil || u == nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.RawPath != "" || strings.Contains(u.Path, "..") {
		return nil, failure(imaging.CodeInvalidInput, imaging.DeliveryNotSent)
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "::1" || u.Hostname() == "localhost")) {
		return nil, failure(imaging.CodeInvalidInput, imaging.DeliveryNotSent)
	}
	if o.HTTPClient == nil || o.SecretResolver == nil || strings.TrimSpace(o.SecretRef) == "" {
		return nil, failure(imaging.CodeInvalidInput, imaging.DeliveryNotSent)
	}
	if o.Profile != "" && (id != OpenAIID || ProfileForBaseURL(o.BaseURL) != o.Profile) {
		return nil, failure(imaging.CodeInvalidInput, imaging.DeliveryNotSent)
	}
	l := o.Limits
	for _, field := range []struct {
		value   *int64
		ceiling int64
	}{
		{&l.MaxRequestBytes, 64 << 20}, {&l.MaxResponseBytes, 64 << 20},
		{&l.MaxImageBytes, 20 << 20}, {&l.MaxPixels, 40_000_000},
	} {
		if *field.value == 0 {
			*field.value = field.ceiling
		}
		if *field.value < 1 || *field.value > field.ceiling {
			return nil, failure(imaging.CodeInvalidInput, imaging.DeliveryNotSent)
		}
	}
	if id == GoogleID && l.MaxRequestBytes > 20_000_000 {
		l.MaxRequestBytes = 20_000_000
	}
	headers, err := customHeaders(o.CustomHeaders)
	if err != nil {
		return nil, err
	}
	c := *o.HTTPClient
	c.Jar = nil
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Adapter{officialOpenAI: id == OpenAIID && strings.EqualFold(u.Hostname(), "api.openai.com"), id: id, profile: o.Profile, client: c, base: strings.TrimRight(u.String(), "/"), secrets: o.SecretResolver, scope: o.Scope, secretRef: o.SecretRef, headers: headers, limits: l}, nil
}

func (a *Adapter) Poll(context.Context, string) (imaging.RemoteResult, error) {
	return imaging.RemoteResult{}, imaging.ErrUnsupported
}
func (a *Adapter) Cancel(context.Context, string) error { return imaging.ErrUnsupported }
func (a *Adapter) Reconcile(context.Context, string) (imaging.RemoteResult, error) {
	return imaging.RemoteResult{}, imaging.ErrUnsupported
}

func failure(code string, delivery imaging.Delivery) error {
	// Do not retain causes: even Unwrap must not expose transport URLs or bodies.
	return &imaging.AdapterError{Code: code, Delivery: delivery}
}
func failed(err error) (imaging.RemoteResult, error) {
	e := err.(*imaging.AdapterError)
	state := "failed"
	if e.Delivery == imaging.DeliveryUncertain {
		state = "unknown"
	}
	return imaging.RemoteResult{State: state, ErrorCode: e.Code}, err
}

func (a *Adapter) Submit(ctx context.Context, r imaging.ExecuteRequest) (imaging.RemoteResult, error) {
	if ctx.Err() != nil {
		return failed(failure(imaging.CodeUnavailable, imaging.DeliveryNotSent))
	}
	path, body, err := a.request(ctx, r)
	if err != nil {
		return failed(err)
	}
	raw, contentType, err := a.encodeRequest(ctx, body)
	if err != nil {
		return failed(err)
	}
	secret, err := a.secrets.ResolveSecret(ctx, a.scope, a.secretRef)
	if err != nil || secret == "" || strings.ContainsAny(secret, "\r\n") {
		return failed(failure(imaging.CodeAuthRequired, imaging.DeliveryNotSent))
	}
	if ctx.Err() != nil {
		return failed(failure(imaging.CodeUnavailable, imaging.DeliveryNotSent))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.base+path, bytes.NewReader(raw))
	if err != nil {
		return failed(failure(imaging.CodeInvalidInput, imaging.DeliveryNotSent))
	}
	// A POST with no GetBody and no idempotency headers is not replayable by the
	// standard transport. There is deliberately no adapter retry loop.
	req.GetBody = nil
	req.Header = a.headers.Clone()
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	if a.id == GoogleID {
		req.Header.Set("x-goog-api-key", secret)
	} else {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return failed(failure(imaging.CodeResultUnknown, imaging.DeliveryUncertain))
	}
	if resp.Body == nil {
		return failed(failure(imaging.CodeResultUnknown, imaging.DeliveryUncertain))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Only unambiguous HTTP rejections are considered rejected. 408, 409,
		// 429, redirects and 5xx are conservatively uncertain without an explicit
		// provider error envelope. Error bodies are bounded and never exposed.
		b, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<10+1))
		return failed(httpFailure(a.id, resp.StatusCode, b, readErr))
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, a.limits.MaxResponseBytes+1))
	if err != nil || int64(len(b)) > a.limits.MaxResponseBytes {
		return failed(failure(imaging.CodeResultUnknown, imaging.DeliveryUncertain))
	}
	return a.response(ctx, b, r.Target.Source == "provider")
}

func httpFailure(id string, status int, b []byte, readErr error) error {
	code, delivery := imaging.CodeResultUnknown, imaging.DeliveryUncertain
	if status >= 400 && status < 500 && status != 408 && status != 409 && status != 429 && status != 499 {
		delivery, code = imaging.DeliveryRejected, imaging.CodeInvalidInput
		switch status {
		case 401:
			code = imaging.CodeAuthRequired
		case 403:
			code = imaging.CodeForbidden
		case 404:
			code = imaging.CodeNotFound
		}
	}
	var envelope struct {
		Error *struct {
			Code    json.RawMessage `json:"code"`
			Status  string          `json:"status"`
			Details []struct {
				Reason string `json:"reason"`
			} `json:"details"`
		} `json:"error"`
	}
	explicit := readErr == nil && len(b) <= 64<<10 && json.Unmarshal(b, &envelope) == nil && envelope.Error != nil && (len(envelope.Error.Code) > 0 || envelope.Error.Status != "")
	if explicit && (status == 429 || status == 409) {
		delivery, code = imaging.DeliveryRejected, imaging.CodeInvalidInput
		if status == 429 {
			code = imaging.CodeRateLimited
		}
	}
	if explicit && id == GoogleID && status == 400 {
		for _, d := range envelope.Error.Details {
			if d.Reason == "API_KEY_INVALID" {
				code = imaging.CodeAuthRequired
			}
		}
	}
	return failure(code, delivery)
}
