package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"syscall"
	"testing"
	"time"
)

func TestClassifyErrorKinds(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want ErrorDetail
	}{
		{"unauthorized", &HTTPError{Status: "401 Unauthorized", StatusCode: 401, Body: `{"error":{"message":"Incorrect API key provided: sk-live-abcdefgh12345678"}}`}, ErrorDetail{Kind: FailureInvalidAPIKey, UpstreamStatus: 401, ProviderMessage: "Incorrect API key provided: [redacted]"}},
		{"forbidden", &HTTPError{Status: "403 Forbidden", StatusCode: 403, Body: "<html>denied</html>"}, ErrorDetail{Kind: FailureInvalidAPIKey, UpstreamStatus: 403}},
		{"not found", fmt.Errorf("request: %w", &HTTPError{Status: "404 Not Found", StatusCode: 404, Body: `{"error":"model missing"}`}), ErrorDetail{Kind: FailureNotFound, UpstreamStatus: 404, ProviderMessage: "model missing"}},
		{"rate limited", &HTTPError{Status: "429 Too Many Requests", StatusCode: 429, Body: `{"message":"slow down"}`}, ErrorDetail{Kind: FailureRateLimited, UpstreamStatus: 429, ProviderMessage: "slow down"}},
		{"context limit", &HTTPError{Status: "400 Bad Request", StatusCode: 400, Body: `{"error":{"message":"maximum context length is 8192 tokens","code":"context_length_exceeded"}}`}, ErrorDetail{Kind: FailureContextLimit, UpstreamStatus: 400, ProviderMessage: "maximum context length is 8192 tokens"}},
		{"bad request", &HTTPError{Status: "422 Unprocessable Entity", StatusCode: 422, Body: `{"detail":"bad tool schema"}`}, ErrorDetail{Kind: FailureBadRequest, UpstreamStatus: 422, ProviderMessage: "bad tool schema"}},
		{"server error", &HTTPError{Status: "503 Service Unavailable", StatusCode: 503}, ErrorDetail{Kind: FailureServerError, UpstreamStatus: 503}},
		{"gateway timeout", &HTTPError{Status: "504 Gateway Timeout", StatusCode: 504}, ErrorDetail{Kind: FailureTimeout, UpstreamStatus: 504}},
		{"stream dropped", (&sseReader{sawData: true}).endError("Chat Completions"), ErrorDetail{Kind: FailureConnectionLost}},
		{"connection reset", &url.Error{Op: "Post", URL: "http://upstream.invalid", Err: syscall.ECONNRESET}, ErrorDetail{Kind: FailureConnectionLost}},
		{"refused", &url.Error{Op: "Post", URL: "http://upstream.invalid", Err: syscall.ECONNREFUSED}, ErrorDetail{Kind: FailureUnreachable}},
		{"deadline", fmt.Errorf("wrapped: %w", context.DeadlineExceeded), ErrorDetail{Kind: FailureTimeout}},
		{"stall", StreamStallError{Idle: time.Minute}, ErrorDetail{Kind: FailureTimeout}},
		{"overloaded in stream", providerStreamError(json.RawMessage(`{"type":"overloaded_error","message":"Overloaded"}`), "request failed"), ErrorDetail{Kind: FailureServerError, ProviderMessage: "Overloaded"}},
		{"rate limit in stream", providerStreamError(json.RawMessage(`{"code":"rate_limit_exceeded","message":"Too many tokens"}`), "request failed"), ErrorDetail{Kind: FailureRateLimited, ProviderMessage: "Too many tokens"}},
		{"other stream error", providerStreamError(nil, "Responses refusal"), ErrorDetail{Kind: FailureProviderError, ProviderMessage: "Responses refusal"}},
		{"unknown", errors.New("tool runtime exploded"), ErrorDetail{Kind: FailureUnknown}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyError(tc.err); got != tc.want {
				t.Fatalf("ClassifyError(%v) = %+v, want %+v", tc.err, got, tc.want)
			}
		})
	}
}

func TestClassifyErrorRedactsLiteralSecrets(t *testing.T) {
	err := &HTTPError{Status: "401 Unauthorized", StatusCode: 401, Body: `{"error":{"message":"key plain-key-value is not valid"}}`}
	if got := ClassifyError(err, "plain-key-value").ProviderMessage; got != "key [redacted] is not valid" {
		t.Fatalf("provider message = %q", got)
	}
}

func TestProviderStreamErrorKeepsItsText(t *testing.T) {
	err := providerStreamError(json.RawMessage(`{"code":"server_busy","message":"Our servers are busy."}`), "request failed")
	if err.Error() != "API stream error: server_busy: Our servers are busy." || retryable(err) {
		t.Fatalf("error = %q retryable=%v", err, retryable(err))
	}
	if err := providerStreamError(nil, "Gemini prompt blocked: SAFETY"); err.Error() != "API stream error: Gemini prompt blocked: SAFETY" {
		t.Fatalf("error = %q", err)
	}
}

func TestErrorDetailIsStoredButNotSent(t *testing.T) {
	in := Message{Role: "assistant", Content: "partial", StopReason: StopReasonError, Error: "boom", ErrorDetail: &ErrorDetail{Kind: FailureRateLimited, UpstreamStatus: 429, ProviderMessage: "slow down"}}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Message
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if out.ErrorDetail == nil || *out.ErrorDetail != *in.ErrorDetail {
		t.Fatalf("round trip lost error_detail: %s", data)
	}
	if sent := stripAuthored([]Message{out}); sent[0].ErrorDetail != nil {
		t.Fatalf("error_detail reached the request: %+v", sent[0])
	}
}
