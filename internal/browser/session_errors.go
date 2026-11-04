package browser

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/chromedp/cdproto"
	"github.com/go-rod/rod/lib/cdp"
)

var ErrTargetClosed = errors.New("original browser target is closed; choose a new browser session explicitly")

// A failed tab switch describes the requested target, not the retained current target.
type tabSwitchError struct {
	targetID string
	cause    error
}

func (e *tabSwitchError) Error() string {
	return fmt.Sprintf("switch to browser target %s failed; current target retained: %v", e.targetID, e.cause)
}

func (e *tabSwitchError) Unwrap() error { return e.cause }

func protocolError(err error) (int, string, bool) {
	var rodErr *cdp.Error
	if errors.As(err, &rodErr) {
		return rodErr.Code, rodErr.Message, true
	}
	var chromeErr *cdproto.Error
	if errors.As(err, &chromeErr) {
		return int(chromeErr.Code), chromeErr.Message, true
	}
	return 0, "", false
}

func isSessionErr(err error) bool {
	code, message, ok := protocolError(err)
	return ok && code == -32001 && (message == "Session with given id not found" || message == "Session with given id not found.")
}

func isTargetClosedErr(err error) bool {
	if errors.Is(err, ErrTargetClosed) {
		return true
	}
	code, message, ok := protocolError(err)
	return ok && ((code == -32602 && message == "No target with given id found") ||
		(code == -32000 && message == "Target closed"))
}

func isConnErr(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if _, _, ok := protocolError(err); ok {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) {
		return true
	}
	var op *net.OpError
	if errors.As(err, &op) {
		return true
	}
	for e := err; e != nil; e = errors.Unwrap(e) {
		msg := strings.ToLower(strings.TrimSpace(e.Error()))
		if strings.HasPrefix(msg, "websocket") && (strings.Contains(msg, "closed") || strings.Contains(msg, "close 100")) {
			return true
		}
		if msg == "eof" || msg == "connection reset by peer" || msg == "broken pipe" {
			return true
		}
	}
	return false
}
