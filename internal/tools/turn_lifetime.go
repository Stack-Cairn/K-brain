package tools

import (
	"context"
	"errors"
)

// ErrTurnEnded is the cancellation cause a host gives a turn's context once the
// turn has finished on its own. Work the turn hands off to run in the background
// (a wait poller, a managed process) outlives that cancellation; any other one,
// such as the user stopping the turn, still ends it.
var ErrTurnEnded = errors.New("turn ended")

// OutliveTurn returns a context with ctx's values that is cancelled together with
// ctx unless ctx ends with ErrTurnEnded. The returned cancel releases it.
func OutliveTurn(ctx context.Context) (context.Context, context.CancelFunc) {
	out, cancel := context.WithCancelCause(context.WithoutCancel(ctx))
	stop := context.AfterFunc(ctx, func() {
		if cause := context.Cause(ctx); !errors.Is(cause, ErrTurnEnded) {
			cancel(cause)
		}
	})
	return out, func() {
		stop()
		cancel(context.Canceled)
	}
}
