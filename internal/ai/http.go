package ai

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

func (c *OpenAI) postJSON(ctx context.Context, path string, body []byte, stream bool, headers http.Header) (*http.Response, error) {
	var resp *http.Response
	err := c.policy().run(ctx, func() error { var err error; resp, err = c.postJSONOnce(ctx, path, body, stream, headers); return err }, nil)
	return resp, err
}

// versionedRoots maps a base URL without an API version to the /v1 root its
// API answered under after the base itself served a web page.
var versionedRoots sync.Map

func (c *OpenAI) postJSONOnce(ctx context.Context, path string, body []byte, stream bool, headers http.Header) (*http.Response, error) {
	observeCacheRequest(ctx, path, body)
	if c.IsFullURL {
		return checkedResponse(c.send(ctx, c.BaseURL, body, stream, headers))
	}
	if root, ok := versionedRoots.Load(c.BaseURL); ok {
		return checkedResponse(c.send(ctx, root.(string)+path, body, stream, headers))
	}
	resp, err := c.send(ctx, c.BaseURL+path, body, stream, headers)
	if err != nil {
		return nil, err
	}
	// A relay configured by its bare host (as earlier LiveAgent versions
	// accepted, adding /v1 themselves) answers API paths at its root with its
	// web page. The model list already falls back to /v1/models for the same
	// reason; requests do the same and keep that root from then on.
	if root, ok := withAPIVersion(c.BaseURL); ok && isWebPage(resp.Header) {
		resp.Body.Close()
		if resp, err = c.send(ctx, root+path, body, stream, headers); err != nil {
			return nil, err
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 && !isWebPage(resp.Header) {
			versionedRoots.Store(c.BaseURL, root)
		}
	}
	return checkedResponse(resp, nil)
}

func (c *OpenAI) send(ctx context.Context, endpoint string, body []byte, stream bool, headers http.Header) (*http.Response, error) {
	if err := checkEndpoint(c.BaseURL, endpoint); err != nil {
		return nil, nonRetryable{err}
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, nonRetryable{err}
	}
	hr.Header.Set("Content-Type", "application/json")
	for name, values := range c.Headers {
		hr.Header[name] = append([]string(nil), values...)
	}
	if !hasHeader(c.Headers, "Authorization") {
		hr.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	if stream {
		hr.Header.Set("Accept", "text/event-stream")
	}
	for name, values := range headers {
		hr.Header[name] = append([]string(nil), values...)
	}
	c.applyCacheHeaders(hr)
	return c.HTTP.Do(hr)
}

// ErrNoBaseURL is a request through a provider that has no API endpoint
// (baseUrl) set: the placeholder provider a fresh configuration starts with,
// or one whose baseUrl was left empty. Nothing can be sent until it is set.
var ErrNoBaseURL = errors.New("no API endpoint (baseUrl) is set for this provider")

// checkEndpoint refuses an endpoint no request can reach — no base URL at
// all, or one without an http/https scheme and host — before anything is
// sent, so it fails at once instead of being retried as a network error.
func checkEndpoint(baseURL, endpoint string) error {
	if strings.TrimSpace(baseURL) == "" {
		return ErrNoBaseURL
	}
	if u, err := url.Parse(endpoint); err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("invalid baseUrl %q: set an http/https API endpoint", baseURL)
	}
	return nil
}

func checkedResponse(resp *http.Response, err error) (*http.Response, error) {
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, newHTTPError(resp, string(b))
	}
	return resp, nil
}

func isWebPage(h http.Header) bool {
	mediaType, _, _ := mime.ParseMediaType(h.Get("Content-Type"))
	return mediaType == "text/html"
}

// withAPIVersion appends /v1 to a base URL whose path does not already end in
// an API version.
func withAPIVersion(base string) (string, bool) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return "", false
	}
	path := strings.TrimRight(u.EscapedPath(), "/")
	if modelCatalogVersionSuffix.MatchString(path) {
		return "", false
	}
	path += "/v1"
	u.Path, _ = url.PathUnescape(path)
	u.RawPath = path
	return u.String(), true
}
