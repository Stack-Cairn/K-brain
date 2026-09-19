package ai

import (
	"bytes"
	"context"
	"io"
	"net/http"
)

func (c *OpenAI) postJSON(ctx context.Context, path string, body []byte, stream bool, headers http.Header) (*http.Response, error) {
	var resp *http.Response
	err := c.policy().run(ctx, func() (err error) {
		resp, err = c.postJSONOnce(ctx, path, body, stream, headers)
		return err
	}, nil)
	return resp, err
}

func (c *OpenAI) postJSONOnce(ctx context.Context, path string, body []byte, stream bool, headers http.Header) (*http.Response, error) {
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, nonRetryable{err}
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Authorization", "Bearer "+c.APIKey)
	if stream {
		hr.Header.Set("Accept", "text/event-stream")
	}
	for name, values := range headers {
		hr.Header[name] = append([]string(nil), values...)
	}
	c.applyCacheHeaders(hr)
	resp, err := c.HTTP.Do(hr)
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
