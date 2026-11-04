package ai

import (
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// defaultStreamIdleTimeout bounds how long a streaming response may stay silent
// (deliver no bytes) before the connection is treated as stalled. Providers can
// pause briefly, but minutes of total silence mean a dead or half-open
// connection that would otherwise hang the turn until the coarse whole-request
// timeout fires — or forever, if the transport has none. Resolved per client
// through idleOr so a client can raise, lower, or disable it.
const defaultStreamIdleTimeout = 120 * time.Second

// idleOr maps a client's configured StreamIdle to an effective timeout:
// zero uses the default and a negative value disables the watchdog.
func idleOr(d time.Duration) time.Duration {
	switch {
	case d < 0:
		return 0
	case d == 0:
		return defaultStreamIdleTimeout
	default:
		return d
	}
}

// errStreamIdle marks a stream the idle watchdog cancelled. It deliberately
// does not wrap context.Canceled/DeadlineExceeded: the retry policy treats a
// stall before output as retryable, whereas a caller cancellation is not.
type errStreamIdle struct {
	idle     time.Duration
	provider string
}

func (e errStreamIdle) Error() string {
	name := e.provider
	if name == "" {
		name = "model"
	}
	return fmt.Sprintf("%s stream stalled: no data received for %s", name, e.idle)
}

// idleStream watches a streaming body. If no bytes arrive within idle it cancels
// the derived request context, which unblocks the in-flight read, and reports
// that cancellation as errStreamIdle rather than the resulting context.Canceled
// so callers can tell a provider stall from a user cancel. A caller cancellation
// of the parent context is passed through unchanged.
type idleStream struct {
	parent   context.Context
	cancel   context.CancelFunc
	idle     time.Duration
	provider string

	body io.Reader

	mu    sync.Mutex
	timer *time.Timer
	fired atomic.Bool
}

// newIdleStream derives the context to issue the streaming request with and a
// guard whose wrap() must be placed around the response body. idle<=0 disables
// the watchdog: the parent context is returned unchanged and wrap is a
// passthrough, so callers can wire it unconditionally.
func newIdleStream(ctx context.Context, idle time.Duration, provider string) (context.Context, *idleStream) {
	if idle <= 0 {
		return ctx, &idleStream{idle: 0}
	}
	sctx, cancel := context.WithCancel(ctx)
	s := &idleStream{parent: ctx, cancel: cancel, idle: idle, provider: provider}
	s.timer = time.AfterFunc(idle, func() {
		s.fired.Store(true)
		cancel()
	})
	return sctx, s
}

// wrap returns a reader that resets the idle clock on progress and converts a
// watchdog cancellation into errStreamIdle.
func (s *idleStream) wrap(r io.Reader) io.Reader {
	if s.idle <= 0 {
		return r
	}
	s.body = r
	return s
}

func (s *idleStream) Read(p []byte) (int, error) {
	n, err := s.body.Read(p)
	if n > 0 {
		s.mu.Lock()
		if s.timer != nil {
			s.timer.Reset(s.idle)
		}
		s.mu.Unlock()
	}
	if err != nil && s.fired.Load() && s.parent.Err() == nil {
		// The watchdog cancelled the request, not the caller: report a stall.
		return n, errStreamIdle{idle: s.idle, provider: s.provider}
	}
	return n, err
}

// close stops the watchdog and releases the derived context. Safe to call once
// via defer whether or not the watchdog ever fired.
func (s *idleStream) close() {
	if s.idle <= 0 {
		return
	}
	s.mu.Lock()
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	s.mu.Unlock()
	s.cancel()
}
