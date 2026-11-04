package browser

import (
	"context"
	"sync/atomic"

	"github.com/chromedp/cdproto"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

func watchChromedpTarget(ctx context.Context, id target.ID, cancel context.CancelCauseFunc) {
	var session atomic.Value
	session.Store(target.SessionID(""))
	chromedp.ListenBrowser(ctx, func(event any) {
		switch event := event.(type) {
		case *target.EventAttachedToTarget:
			if event.TargetInfo != nil && event.TargetInfo.TargetID == id {
				session.Store(event.SessionID)
			}
		case *target.EventDetachedFromTarget:
			if event.SessionID != "" && event.SessionID == session.Load().(target.SessionID) {
				cancel(&cdproto.Error{Code: -32001, Message: "Session with given id not found"})
			}
		case *target.EventTargetDestroyed:
			if event.TargetID == id {
				cancel(ErrTargetClosed)
			}
		}
	})
}

func (b *chromedpBackend) targetError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil && b.targetCtx != nil && context.Cause(b.targetCtx) != nil {
		err = context.Cause(b.targetCtx)
	}
	if isSessionErr(err) {
		_, checkErr := target.GetTargetInfo().WithTargetID(b.targetID).Do(cdp.WithExecutor(ctx, b.browser))
		if isTargetClosedErr(checkErr) {
			return ErrTargetClosed
		}
		if checkErr != nil {
			return checkErr
		}
	}
	return err
}
