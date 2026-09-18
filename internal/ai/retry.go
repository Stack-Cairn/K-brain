package ai

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type retryPolicy struct {
	attempts int
	onRetry  func(RetryEvent)
}

func newRetryPolicy(maxRetries int, onRetry func(RetryEvent)) retryPolicy {
	if maxRetries <= 0 {
		maxRetries = DefaultMaxAttempts
	}
	return retryPolicy{attempts: maxRetries, onRetry: onRetry}
}

func (p retryPolicy) run(ctx context.Context, once func() error, stop func() bool) error {
	var last error
	for attempt := 1; attempt <= p.attempts; attempt++ {
		err := once()
		if err == nil {
			return nil
		}
		last = err
		if (stop != nil && stop()) || !retryable(err) || attempt == p.attempts {
			break
		}
		delay := retryDelay(attempt, err)
		if p.onRetry != nil {
			p.onRetry(RetryEvent{Attempt: attempt, Max: p.attempts, Delay: delay, Err: err})
		}
		if serr := sleep(ctx, delay); serr != nil {
			return serr
		}
	}
	return last
}

const maxRetryAfter = 60 * time.Second

func retryDelay(attempt int, err error) time.Duration {
	d := backoff(attempt)
	if he, ok := errors.AsType[*HTTPError](err); ok && he.RetryAfter > d {
		d = he.RetryAfter
	}
	return d
}

func newHTTPError(resp *http.Response, body string) *HTTPError {
	return &HTTPError{Status: resp.Status, Body: strings.TrimSpace(body), RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
}

func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		return time.Duration(max(secs, 0)) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(time.Until(t), 0)
	}
	return 0
}
