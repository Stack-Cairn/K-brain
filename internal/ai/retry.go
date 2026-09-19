package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
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
		if err := ctx.Err(); err != nil {
			return err
		}
		err := once()
		if err == nil {
			return nil
		}
		last = err
		if err := ctx.Err(); err != nil {
			return err
		}
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
	if he, ok := errors.AsType[*HTTPError](err); ok && (he.RetryAfterSet || he.RetryAfter > 0) {
		return max(he.RetryAfter, 0)
	}
	return backoff(attempt)
}

func newHTTPError(resp *http.Response, body string) *HTTPError {
	delay, found := parseRetryDuration(resp.Header.Get("Retry-After-Ms"), time.Millisecond)
	if !found {
		delay, found = retryAfter(resp.Header.Get("Retry-After"))
	}
	return &HTTPError{Status: resp.Status, StatusCode: resp.StatusCode, Body: strings.TrimSpace(body),
		RetryAfter: delay, RetryAfterSet: found, ShouldRetry: strings.ToLower(strings.TrimSpace(resp.Header.Get("X-Should-Retry")))}
}

func parseRetryAfter(v string) time.Duration {
	delay, _ := retryAfter(v)
	return delay
}

func retryAfter(v string) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if delay, found := parseRetryDuration(v, time.Second); found {
		return delay, true
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(time.Until(t), 0), true
	}
	return 0, false
}

func parseRetryDuration(v string, unit time.Duration) (time.Duration, bool) {
	n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 {
		return 0, false
	}
	n *= float64(unit)
	if n >= float64(math.MaxInt64) {
		return time.Duration(math.MaxInt64), true
	}
	return time.Duration(n), true
}

type HTTPError struct {
	Status      string
	Body        string
	StatusCode  int
	ShouldRetry string

	RetryAfter    time.Duration
	RetryAfterSet bool
}

func (e *HTTPError) Error() string {
	message := e.Status + ": " + e.Body
	if e.RetryAfter > maxRetryAfter {
		message += fmt.Sprintf(" (server retry delay %s exceeds automatic retry limit %s)", e.RetryAfter, maxRetryAfter)
	}
	return message
}

const DefaultMaxAttempts = 8

type RetryEvent struct {
	Attempt int
	Max     int
	Delay   time.Duration
	Err     error
}

func retryableStatus(code int) bool {
	return code == http.StatusRequestTimeout || code == http.StatusConflict || code == http.StatusTooManyRequests || code >= 500 && code <= 599
}

type nonRetryable struct{ err error }

func (n nonRetryable) Error() string { return n.err.Error() }
func (n nonRetryable) Unwrap() error { return n.err }

func retryable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if _, ok := errors.AsType[nonRetryable](err); ok {
		return false
	}
	if _, ok := errors.AsType[*OutputLimitError](err); ok {
		return false
	}
	if _, ok := errors.AsType[*json.SyntaxError](err); ok {
		return false
	}
	if _, ok := errors.AsType[*json.UnmarshalTypeError](err); ok {
		return false
	}
	if he, ok := errors.AsType[*HTTPError](err); ok {
		if he.RetryAfter > maxRetryAfter {
			return false
		}
		if he.ShouldRetry == "true" {
			return true
		}
		if he.ShouldRetry == "false" {
			return false
		}
		code := he.StatusCode
		if fields := strings.Fields(he.Status); code == 0 && len(fields) > 0 {
			code, _ = strconv.Atoi(fields[0])
		}
		return retryableStatus(code)
	}

	return true
}

func backoff(attempt int) time.Duration {
	d := min(time.Second<<min(max(attempt-1, 0), 5), 20*time.Second)
	return d + time.Duration(rand.Int64N(int64(d/4)+1))
}

var sleep = func(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
