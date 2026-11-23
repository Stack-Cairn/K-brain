package ai

import (
	"context"
	"errors"
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

// StreamStallError marks a stream the idle watchdog cancelled. It deliberately
// does not wrap context.Canceled/DeadlineExceeded: the retry policy treats a
// stall before output as retryable, whereas a caller cancellation is not.
type StreamStallError struct {
	Idle     time.Duration
	Provider string
}

func (e StreamStallError) Error() string {
	name := e.Provider
	if name == "" {
		name = "model"
	}
	return fmt.Sprintf("%s stream stalled: no data received for %s", name, e.Idle)
}

// StreamStall reports whether err is the idle watchdog giving up on a
// response that sent nothing for too long, and for how long it waited.
func StreamStall(err error) (time.Duration, bool) {
	if e, ok := errors.AsType[StreamStallError](err); ok {
		return e.Idle, true
	}
	return 0, false
}

// idleStream watches a streaming body. If no bytes arrive within idle it cancels
// the derived request context, which unblocks the in-flight read, and reports
// that cancellation as StreamStallError rather than the resulting context.Canceled
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
// watchdog cancellation into StreamStallError.
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
	return n, s.stalled(err)
}

// stalled turns err into StreamStallError when the watchdog, not the caller,
// cancelled the request. It covers the request itself as well as the body:
// a provider that never sends response headers fails the request with the
// derived context's cancellation, which must not read as the user stopping
// the turn (and must stay retryable).
func (s *idleStream) stalled(err error) error {
	if err != nil && s.idle > 0 && s.fired.Load() && s.parent.Err() == nil {
		return StreamStallError{Idle: s.idle, Provider: s.provider}
	}
	return err
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

// streamCallbacks are a streaming call's output callbacks.
type streamCallbacks struct {
	text, think func(string)
	tool        func(id, name, args string)
}

// retryStalled runs a stream whose client has no retry around the whole
// stream (only around sending the request): a stall before anything was
// emitted — typically a provider that never sends response headers — is
// retried under p like any transient failure. Once output has reached the
// caller, or for any other error, the result stands.
func retryStalled(ctx context.Context, p retryPolicy, cb streamCallbacks, once func(streamCallbacks) (Message, Usage, error)) (Message, Usage, error) {
	var msg Message
	var usage Usage
	var last error
	emitted := false
	wrapped := streamCallbacks{
		text: func(s string) {
			emitted = true
			if cb.text != nil {
				cb.text(s)
			}
		},
		think: func(s string) {
			emitted = true
			if cb.think != nil {
				cb.think(s)
			}
		},
		tool: func(id, name, args string) {
			emitted = true
			if cb.tool != nil {
				cb.tool(id, name, args)
			}
		},
	}
	err := p.run(ctx, func() error {
		msg, usage, last = once(wrapped)
		return last
	}, func() bool {
		_, stall := StreamStall(last)
		return !stall || emitted
	})
	return msg, usage, err
}
