package acp

import "context"

func (b *Bridge) turnContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if b.conn == nil {
		return context.WithCancel(ctx)
	}
	base := context.WithoutCancel(ctx)
	var turnCtx context.Context
	var cancel context.CancelFunc
	if deadline, ok := ctx.Deadline(); ok {
		turnCtx, cancel = context.WithDeadline(base, deadline)
	} else {
		turnCtx, cancel = context.WithCancel(base)
	}
	if ctx.Err() != nil {
		cancel()
	}
	disconnected := b.conn.Done()
	select {
	case <-disconnected:
		cancel()
	default:
		go func() {
			select {
			case <-disconnected:
				cancel()
			case <-turnCtx.Done():
			}
		}()
	}
	return turnCtx, cancel
}
