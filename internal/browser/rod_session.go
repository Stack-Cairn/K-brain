package browser

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/cdp"
	"github.com/go-rod/rod/lib/proto"
)

func (b *Browser) keepBrowser(ctx context.Context, rb *rod.Browser) {
	life, cancel := context.WithCancel(context.WithoutCancel(ctx))
	b.browser = rb.Context(life)
	b.browserCancel = cancel
}

// Watch before attaching: a closing target can still appear in getTargets, and
// commands on its detached session need not receive a protocol response.
// The watcher owns only its page context, and survives individual calls.
func (b *Browser) targetLife(targetID proto.TargetTargetID) (context.Context, context.CancelFunc, *atomic.Value) {
	life, cancel := context.WithCancelCause(b.browser.GetContext())
	events := b.browser.Context(life).Event()
	var session atomic.Value
	session.Store(proto.TargetSessionID(""))
	done := make(chan struct{})
	go func() {
		defer close(done)
		for msg := range events {
			var destroyed proto.TargetTargetDestroyed
			if msg.Load(&destroyed) && destroyed.TargetID == targetID {
				cancel(ErrTargetClosed)
				return
			}
			var detached proto.TargetDetachedFromTarget
			if msg.Load(&detached) && (detached.TargetID == targetID ||
				detached.SessionID == session.Load().(proto.TargetSessionID)) {
				cancel(&cdp.Error{Code: -32001, Message: "Session with given id not found"})
				return
			}
		}
	}()
	return life, func() { cancel(nil); <-done }, &session
}

// Rod's Context replaces the page context. Merge both lifetimes explicitly so
// target destruction interrupts a health check without imposing a new timeout.
func pageFrameTree(ctx context.Context, p *rod.Page) error {
	call, cancel := context.WithCancelCause(ctx)
	stop := context.AfterFunc(p.GetContext(), func() { cancel(context.Cause(p.GetContext())) })
	defer stop()
	defer cancel(nil)
	_, err := (proto.PageGetFrameTree{}).Call(p.Context(call))
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil && context.Cause(p.GetContext()) != nil {
		return context.Cause(p.GetContext())
	}
	return err
}

// A page may also be canceled by Rod's own detach listener before our watcher
// runs. Confirm the original target on the browser connection in that case.
func (b *Browser) targetError(ctx context.Context, targetID proto.TargetTargetID, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if !errors.Is(err, context.Canceled) && !isSessionErr(err) {
		return err
	}
	list, checkErr := (proto.TargetGetTargets{}).Call(b.browser.Context(ctx))
	if checkErr != nil {
		return checkErr
	}
	for _, info := range list.TargetInfos {
		if info.TargetID == targetID {
			if errors.Is(err, context.Canceled) {
				return &cdp.Error{Code: -32001, Message: "Session with given id not found"}
			}
			return err
		}
	}
	return fmt.Errorf("%w: %s", ErrTargetClosed, targetID)
}

func (b *Browser) attachTarget(ctx context.Context, targetID proto.TargetTargetID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if b.staleSession != "" && b.mode != ModeExtension {
		_ = (proto.TargetDetachFromTarget{SessionID: b.staleSession}).Call(b.browser.Context(ctx))
	}
	b.staleSession = ""
	b.browser.RemoveState(targetID)
	life, cancel, session := b.targetLife(targetID)
	stop := context.AfterFunc(ctx, cancel)
	// Target destruction events require discovery on the browser connection.
	err := (proto.TargetSetDiscoverTargets{Discover: true}).Call(b.browser.Context(life))
	var p *rod.Page
	if err == nil {
		p, err = b.browser.Context(life).PageFromTarget(targetID)
	}
	if err == nil {
		session.Store(p.SessionID)
		err = pageFrameTree(ctx, p)
	}
	stopped := stop()
	if !stopped && ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		if p != nil {
			b.staleSession = p.SessionID
		}
		if ctx.Err() == nil && context.Cause(life) != nil {
			err = context.Cause(life)
		}
		err = b.targetError(ctx, targetID, err)
		cancel()
		b.browser.RemoveState(targetID)
		if isTargetClosedErr(err) {
			return fmt.Errorf("%w: %s: %w", ErrTargetClosed, targetID, err)
		}
		return fmt.Errorf("attach original target %s: %w", targetID, err)
	}
	b.page, b.targetID, b.pageCancel = p, targetID, cancel
	return nil
}

func (b *Browser) Invalidate(err error) {
	var switchErr *tabSwitchError
	if errors.As(err, &switchErr) && !isConnErr(err) {
		return
	}
	if b.page != nil {
		b.staleSession = b.page.SessionID
	}
	if b.pageCancel != nil {
		b.pageCancel()
		b.pageCancel = nil
	}
	if b.browser != nil {
		b.browser.RemoveState(b.targetID)
	}
	b.page = nil
	b.reconnect = b.reconnect || isConnErr(err)
}

func (b *Browser) reconnectOriginal(ctx context.Context) error {
	if b.endpoint == "" {
		return fmt.Errorf("original browser endpoint is unavailable; no different browser was selected")
	}
	if b.browser != nil {
		detach(b.browser)
	}
	if b.browserCancel != nil {
		b.browserCancel()
	}
	rb, err := connectLive(ctx, b.endpoint)
	if err != nil {
		return fmt.Errorf("reconnect original browser; no profile was changed: %w", err)
	}
	b.keepBrowser(ctx, rb)
	b.staleSession = ""
	b.reconnect = false
	return nil
}

// Prepare performs only protocol checks and attachment, before any script action.
func (b *Browser) Prepare(ctx context.Context) error {
	if b.browser == nil || b.targetID == "" {
		return fmt.Errorf("browser session has no original target")
	}
	var last error
	for attempt := 0; attempt < 2; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if b.reconnect {
			if err := b.reconnectOriginal(ctx); err != nil {
				return err
			}
		}
		list, err := (proto.TargetGetTargets{}).Call(b.browser.Context(ctx))
		if err != nil {
			if isConnErr(err) && attempt == 0 {
				b.Invalidate(err)
				last = err
				continue
			}
			return err
		}
		found := false
		for _, info := range list.TargetInfos {
			if info.TargetID == b.targetID {
				found = true
				break
			}
		}
		if !found || (b.page != nil && isTargetClosedErr(context.Cause(b.page.GetContext()))) {
			b.Invalidate(ErrTargetClosed)
			return fmt.Errorf("%w: %s", ErrTargetClosed, b.targetID)
		}
		if b.page != nil && b.page.GetContext().Err() == nil {
			err = b.targetError(ctx, b.targetID, pageFrameTree(ctx, b.page))
			if err == nil {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if isTargetClosedErr(err) {
				b.Invalidate(err)
				return fmt.Errorf("%w: %s: %w", ErrTargetClosed, b.targetID, err)
			}
			if !isSessionErr(err) && !isConnErr(err) {
				return err
			}
			b.Invalidate(err)
			if b.reconnect {
				last = err
				continue
			}
		} else if b.page != nil {
			b.Invalidate(nil)
		}
		err = b.attachTarget(ctx, b.targetID)
		if err == nil {
			return nil
		}
		last = err
		if !isConnErr(err) && !isSessionErr(err) {
			return err
		}
		b.Invalidate(err)
	}
	return fmt.Errorf("prepare original target: %w", last)
}
