package browser

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/cdp"
)

// errLiveRefused marks a live candidate that answered, but not like a
// debuggable browser (stale DevToolsActivePort, port reused by another app).
var errLiveRefused = errors.New("live candidate refused")

// connectLive opens the CDP connection to the user's running browser. Chrome's
// chrome://inspect remote-debugging switch shows an "Allow remote debugging?"
// prompt and holds the connection until the user answers, so the wait is
// bounded by ctx (the tool call budget) rather than a short fixed timeout.
// The resulting connection outlives ctx: the session keeps it for later calls,
// so the user approves once per session instead of once per call.
var connectLive = func(ctx context.Context, wsURL string) (*rod.Browser, error) {
	u, err := url.Parse(wsURL)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errLiveRefused, err)
	}
	dialer := &trackingDialer{tls: u.Scheme == "wss"}
	if dialer.tls && u.Port() == "" {
		wsURL = (&url.URL{Scheme: u.Scheme, User: u.User, Host: u.Host + ":443", Path: u.Path, RawQuery: u.RawQuery}).String()
	}

	type result struct {
		browser *rod.Browser
		err     error
	}
	done := make(chan result, 1)
	go func() {
		ws := &cdp.WebSocket{Dialer: dialer}
		if err := ws.Connect(ctx, wsURL, nil); err != nil {
			done <- result{err: err}
			return
		}
		rb := rod.New().Client(cdp.New().Start(ws))
		if err := rb.Connect(); err != nil {
			_ = ws.Close()
			done <- result{err: err}
			return
		}
		done <- result{browser: rb}
	}()

	select {
	case r := <-done:
		if r.err == nil {
			return r.browser, nil
		}
		return nil, classifyLiveConnectErr(ctx, r.err)
	case <-ctx.Done():
		dialer.close()
		go func() {
			// Never Browser.Close here: that would quit the user's browser.
			if r := <-done; r.browser != nil {
				detach(r.browser)
			}
		}()
		return nil, fmt.Errorf("%w: timed out waiting for the browser to accept the connection — click Allow on Chrome's 'Allow remote debugging?' prompt, then retry (approval lasts for the rest of the session)", ErrPermissionBlocked)
	}
}

func classifyLiveConnectErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("%w: timed out waiting for the browser to accept the connection — click Allow on Chrome's 'Allow remote debugging?' prompt, then retry: %v", ErrPermissionBlocked, err)
	}
	var bad *cdp.BadHandshakeError
	if errors.As(err, &bad) && strings.HasPrefix(bad.Status, "403") {
		return fmt.Errorf("%w: the browser rejected remote debugging (%s) — enable chrome://inspect/#remote-debugging and click Allow, then retry", ErrPermissionBlocked, bad.Status)
	}
	return fmt.Errorf("%w: %v", errLiveRefused, err)
}

// trackingDialer remembers the connection it opened so a pending handshake —
// which rod reads without honoring ctx — can be aborted when ctx ends.
type trackingDialer struct {
	tls    bool
	mu     sync.Mutex
	conn   net.Conn
	closed bool
}

func (d *trackingDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	var (
		conn net.Conn
		err  error
	)
	if d.tls {
		conn, err = (&tls.Dialer{}).DialContext(ctx, network, address)
	} else {
		conn, err = (&net.Dialer{}).DialContext(ctx, network, address)
	}
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		_ = conn.Close()
		return nil, context.Canceled
	}
	d.conn = conn
	return conn, nil
}

func (d *trackingDialer) close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closed = true
	if d.conn != nil {
		_ = d.conn.Close()
	}
}
