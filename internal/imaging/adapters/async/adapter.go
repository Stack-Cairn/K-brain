// Package asyncadapter implements evidence-scoped asynchronous image protocols.
// Factories do not register or enable production targets. See README.md.
package asyncadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

const (
	JimengID     = "jimeng-image"
	RunningHubID = "runninghub-workflow"
	Version      = "1"
	maxResponse  = 8 << 20
	maxUpload    = 20 << 20 // Local policy, not a documented RunningHub limit.
)

// Options must come from trusted backend configuration. HTTPClient's transport
// must enforce network policy and must not retry, log, or inject credentials.
// ResolvedConnection provides the existing HTTP and scoped secret contracts.
type Options struct {
	Connection       imaging.ResolvedConnection
	Scope            imaging.Scope
	BaseURL          string
	SecretRef        string // RunningHub API key
	AccessKeyRef     string // Jimeng AK
	SecretKeyRef     string // Jimeng SK
	SecurityTokenRef string // Optional Jimeng STS
	Now              func() time.Time
}

type transport struct {
	client     http.Client
	base       string
	connection imaging.ResolvedConnection
	scope      imaging.Scope
}

func newTransport(o Options, defaultBase string) (transport, error) {
	if o.BaseURL == "" {
		o.BaseURL = defaultBase
	}
	u, err := url.Parse(o.BaseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.RawPath != "" || u.Opaque != "" {
		return transport{}, failure(imaging.CodeInvalidInput, imaging.DeliveryNotSent)
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "::1" || u.Hostname() == "localhost")) {
		return transport{}, failure(imaging.CodeInvalidInput, imaging.DeliveryNotSent)
	}
	if o.Connection.HTTPClient == nil || o.Connection.SecretResolver == nil {
		return transport{}, failure(imaging.CodeInvalidInput, imaging.DeliveryNotSent)
	}
	c := *o.Connection.HTTPClient
	c.Jar = nil
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if c.Timeout <= 0 {
		c.Timeout = 60 * time.Second
	}
	return transport{client: c, base: strings.TrimRight(u.String(), "/"), connection: o.Connection, scope: o.Scope}, nil
}

func failure(code string, d imaging.Delivery) error {
	return &imaging.AdapterError{Code: code, Delivery: d}
}
func failureReason(code string, d imaging.Delivery, reason string) error {
	return &imaging.AdapterError{Code: code, Delivery: d, Reason: reason}
}
func resultError(handle string, err error) (imaging.RemoteResult, error) {
	state := "failed"
	e, ok := err.(*imaging.AdapterError)
	code := imaging.CodeUnavailable
	reason := ""
	if ok {
		code = e.Code
		reason = e.Reason
		if e.Delivery == imaging.DeliveryUncertain {
			state = "unknown"
		}
	}
	if handle != "" {
		state = "unknown"
	}
	return imaging.RemoteResult{State: state, Handle: handle, ErrorCode: code, ErrorReason: reason}, err
}
func unknown(handle string, code string) imaging.RemoteResult {
	return imaging.RemoteResult{State: "unknown", Handle: handle, ErrorCode: code}
}
func pending(handle string) imaging.RemoteResult {
	return imaging.RemoteResult{State: "pending", Handle: handle, NextPollAfter: 3 * time.Second}
}
func (t *transport) secret(ctx context.Context, ref string) (string, error) {
	if ref == "" {
		return "", failure(imaging.CodeAuthRequired, imaging.DeliveryNotSent)
	}
	s, err := t.connection.SecretResolver.ResolveSecret(ctx, t.scope, ref)
	if err != nil || strings.TrimSpace(s) == "" || strings.ContainsAny(s, "\r\n") {
		return "", failure(imaging.CodeAuthRequired, imaging.DeliveryNotSent)
	}
	return s, nil
}

// do never retries. Once Do starts a transport/parse failure is uncertain.
// No upstream error text, URL, request ID, or cause is retained in errors.
func (t *transport) do(req *http.Request, dst any) error {
	if req.Context().Err() != nil {
		return failure(imaging.CodeUnavailable, imaging.DeliveryNotSent)
	}
	resp, err := t.client.Do(req)
	if err != nil {
		reason := "no response from the provider"
		if why := imaging.TransportReason(err); why != "" {
			reason += ": " + why
		}
		return failureReason(imaging.CodeResultUnknown, imaging.DeliveryUncertain, reason)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		code, d := imaging.CodeUnavailable, imaging.DeliveryUncertain
		switch resp.StatusCode {
		case 400, 404, 422:
			code, d = imaging.CodeInvalidInput, imaging.DeliveryRejected
		case 401:
			code, d = imaging.CodeAuthRequired, imaging.DeliveryRejected
		case 403:
			code, d = imaging.CodeForbidden, imaging.DeliveryRejected
		case 429:
			code, d = imaging.CodeRateLimited, imaging.DeliveryRejected
		}
		return failureReason(code, d, "provider returned HTTP "+strconv.Itoa(resp.StatusCode))
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil || len(raw) > maxResponse {
		return failure(imaging.CodeResultUnknown, imaging.DeliveryUncertain)
	}
	if json.Unmarshal(raw, dst) != nil {
		return failure(imaging.CodeResultUnknown, imaging.DeliveryUncertain)
	}
	return nil
}
func (t *transport) bearerJSON(ctx context.Context, path, key string, body any, dst any) error {
	raw, err := json.Marshal(body)
	if err != nil || len(raw) > maxResponse {
		return failure(imaging.CodeInvalidInput, imaging.DeliveryNotSent)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.base+path, bytes.NewReader(raw))
	if err != nil {
		return failure(imaging.CodeInvalidInput, imaging.DeliveryNotSent)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	return t.do(req, dst)
}
func validHandle(h string) bool {
	return strings.TrimSpace(h) != "" && len(h) <= 2048 && !strings.ContainsAny(h, "\r\n\x00")
}
func validOutputURL(s string) bool {
	u, e := url.Parse(s)
	return e == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.Fragment == ""
}
func imageMIME(s string) string {
	switch strings.ToLower(s) {
	case "png", "image/png":
		return "image/png"
	case "jpg", "jpeg", "image/jpeg":
		return "image/jpeg"
	case "webp", "image/webp":
		return "image/webp"
	}
	return ""
}
