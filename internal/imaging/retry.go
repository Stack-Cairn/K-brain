package imaging

import (
	"context"
	"errors"
	"time"
)

func (s *Service) backoff(failures int, hint time.Duration) time.Duration {
	d := s.options.PollInterval
	for i := 1; i < failures && d < s.options.MaxPollInterval; i++ {
		d *= 2
	}
	if d > s.options.MaxPollInterval {
		d = s.options.MaxPollInterval
	}
	if hint > d {
		d = hint
	}
	return d
}
func safeEngineError(err error) *ImageError {
	var ie *ImageError
	if errors.As(err, &ie) {
		return PublicError(ie)
	}
	var ae *AdapterError
	if errors.As(err, &ae) {
		return PublicError(&ImageError{Code: ae.Code, Reason: ae.Reason})
	}
	if errors.Is(err, ErrUnsupported) {
		return PublicError(&ImageError{Code: CodeUnsupportedOperation, Reason: "operation is not supported by this connection"})
	}
	return PublicError(&ImageError{Code: CodeUnavailable, Reason: TransportReason(err)})
}

// outputSaveError reports a lost provider output under its own code whatever broke the
// save. A forbidden or unavailable code here would describe the module, not the lost image.
func outputSaveError(err error) *ImageError {
	e := safeEngineError(err)
	reason := e.Reason
	if e.Code == CodeTooLarge {
		reason = "generated image exceeds the size limit"
	}
	if reason == "" && e.Code != CodeUnavailable {
		reason = errorMessage(e.Code)
	}
	if reason == "" {
		reason = "generated image could not be saved"
	}
	return PublicError(&ImageError{Code: CodeOutputSaveFailed, Reason: reason})
}

// lastAttemptReason keeps the submit's explanation when a later step can only say "unknown".
func lastAttemptReason(x ItemExecution, fallback string) string {
	if n := len(x.Attempts); n > 0 && x.Attempts[n-1].Error != nil && x.Attempts[n-1].Error.Reason != "" {
		return x.Attempts[n-1].Error.Reason
	}
	return fallback
}

// Timers use the injected clock so deadlines, backoff and lease tests need no wall clock waits.
func (s *Service) timedContext(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	timer := s.clock.After(d)
	go func() {
		select {
		case <-ctx.Done():
		case <-timer:
			cancel()
		}
	}()
	return ctx, cancel
}
func (s *Service) waitRetry(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.clock.After(d):
		return nil
	}
}
