package ai

import (
	"context"
	"errors"
	"net/url"
	"testing"
)

// A provider with no baseUrl (the placeholder a fresh configuration starts
// with) fails at once with ErrNoBaseURL instead of being retried as a
// network error for over a minute. (The anthropic client adds its version
// path to the base, so it reports the base as invalid instead.)
func TestStreamWithoutBaseURLFailsAtOnce(t *testing.T) {
	noSleep(t)
	for _, api := range []string{"chat", "responses", "anthropic"} {
		var retries int
		onRetry := func(RetryEvent) { retries++ }
		var err error
		switch api {
		case "chat":
			c := New("", "")
			c.OnRetry = onRetry
			_, _, err = c.Stream(context.Background(), Request{Model: "m"}, nil, nil, nil)
		case "responses":
			c := &Responses{OpenAI: New("", "")}
			c.OnRetry = onRetry
			_, _, err = c.Stream(context.Background(), Request{Model: "m"}, nil, nil, nil)
		case "anthropic":
			c := newAnthropicWithOptions("", "", false, "", nil)
			c.OnRetry = onRetry
			_, _, err = c.Stream(context.Background(), Request{Model: "m"}, nil, nil, nil)
		}
		if err == nil || (api != "anthropic" && !errors.Is(err, ErrNoBaseURL)) {
			t.Errorf("%s: err = %v, want ErrNoBaseURL", api, err)
		}
		if retries != 0 {
			t.Errorf("%s: retried %d times, want none", api, retries)
		}
	}
}

func TestUnreachableURLErrorsAreNotRetried(t *testing.T) {
	for _, raw := range []string{"/chat/completions", "htp://host/v1", "http:///v1"} {
		err := &url.Error{Op: "Post", URL: raw, Err: errors.New(`unsupported protocol scheme ""`)}
		if retryable(err) {
			t.Errorf("retryable(%q) = true, want false", raw)
		}
	}
	if !retryable(&url.Error{Op: "Post", URL: "https://api.example.test/v1", Err: errors.New("connection reset")}) {
		t.Error("a transport error against a real host should still be retried")
	}
}
