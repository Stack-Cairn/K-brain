package browser

import (
	"context"
	"errors"

	"github.com/go-rod/rod"
)

// Rod's Context replaces the page context, so every page action must explicitly
// retain the connection, session and target lifetime as well as its caller's.
// Execute once: recovery belongs to Prepare before a subsequent action.
func rodPageCall[T any](ctx context.Context, b *Browser, fn func(*rod.Page) (T, error)) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	p, targetID := b.page, b.targetID
	if p == nil {
		return zero, errors.New("no attached tab")
	}
	life := p.GetContext()
	if err := context.Cause(life); err != nil {
		return zero, b.targetError(ctx, targetID, err)
	}
	call, cancel := context.WithCancelCause(ctx)
	stop := context.AfterFunc(life, func() { cancel(context.Cause(life)) })
	defer cancel(nil)
	defer stop()
	value, err := fn(p.Context(call))
	// Release the callback as soon as the command completes, before an error
	// classification may query the browser connection.
	stop()
	if err != nil && context.Cause(life) != nil {
		err = context.Cause(life)
	}
	return value, b.targetError(ctx, targetID, err)
}

func rodPageAction(ctx context.Context, b *Browser, fn func(*rod.Page) error) error {
	_, err := rodPageCall(ctx, b, func(p *rod.Page) (struct{}, error) {
		return struct{}{}, fn(p)
	})
	return err
}
