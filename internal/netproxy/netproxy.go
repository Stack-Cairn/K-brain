// Package netproxy selects the outbound transport for provider HTTP traffic.
//
// The package runs in one of two modes, decided per request:
//
//   - Managed: LiveAgent passed EnvVar at spawn or Set has been called.
//     Providers whose useSystemProxy toggle is on use the app proxy; all
//     others connect directly and HTTPS_PROXY/HTTP_PROXY/NO_PROXY are ignored.
//   - Standalone: EnvVar is unset and Set has not been called. Both toggles
//     resolve the proxy from HTTPS_PROXY/HTTP_PROXY/NO_PROXY, matching
//     http.DefaultTransport.
//
// Loopback hosts are never proxied. Proxy URLs and credentials are never
// logged or included in errors.
package netproxy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"golang.org/x/net/http/httpproxy"
)

// EnvVar carries the LiveAgent app proxy into the backend process. Values:
// a proxy URL (http, https, socks5, socks5h), "direct" for app proxy disabled,
// or "invalid" for an enabled but unusable app proxy. Any value selects
// managed mode; when unset the package starts in standalone mode and uses the
// standard HTTPS_PROXY/HTTP_PROXY/NO_PROXY variables for every provider.
const EnvVar = "LIVEAGENT_KBRAIN_PROXY"

const (
	valueDirect  = "direct"
	valueInvalid = "invalid"
	noProxy      = "localhost,127.0.0.1,::1"
)

// ErrInvalidProxy fails proxied requests while the app proxy is enabled but
// misconfigured, instead of silently falling back to a direct connection.
var ErrInvalidProxy = errors.New("app proxy is enabled but its configuration is invalid")

// state is the published proxy configuration. managed is false only for the
// standalone environment state built when EnvVar is unset.
type state struct {
	proxy   func(*url.URL) (*url.URL, error)
	managed bool
}

var (
	current    atomic.Pointer[state]
	initOnce   sync.Once
	transports struct {
		once            sync.Once
		proxied, direct *http.Transport
	}
)

func load() *state {
	initOnce.Do(func() {
		if current.Load() == nil {
			v, ok := os.LookupEnv(EnvVar)
			if !ok {
				cfg := httpproxy.FromEnvironment()
				current.Store(&state{proxy: wrap(cfg.ProxyFunc())})
				return
			}
			st, _ := parse(v)
			current.Store(st)
		}
	})
	return current.Load()
}

// Set replaces the proxy used by proxied providers and switches the package
// to managed mode. It applies to subsequent requests without rebuilding
// clients. An unparseable value is stored as invalid (proxied requests fail)
// and reported to the caller.
func Set(value string) error {
	st, err := parse(value)
	// Publish a state before releasing concurrent initialization waiters.
	initOnce.Do(func() { current.Store(st) })
	current.Store(st)
	return err
}

func parse(value string) (*state, error) {
	value = strings.TrimSpace(value)
	switch strings.ToLower(value) {
	case "", valueDirect:
		return &state{proxy: func(*url.URL) (*url.URL, error) { return nil, nil }, managed: true}, nil
	case valueInvalid:
		return invalid(), nil
	}
	u, err := url.Parse(value)
	if err != nil || u.Hostname() == "" || u.Port() == "" {
		return invalid(), ErrInvalidProxy
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return invalid(), ErrInvalidProxy
	}
	switch u.Scheme {
	case "http", "https", "socks5":
	case "socks5h":
		// net/http's SOCKS5 dialer already resolves the target remotely.
		u.Scheme = "socks5"
	default:
		return invalid(), ErrInvalidProxy
	}
	cfg := httpproxy.Config{HTTPProxy: u.String(), HTTPSProxy: u.String(), NoProxy: noProxy}
	return &state{proxy: wrap(cfg.ProxyFunc()), managed: true}, nil
}

func invalid() *state {
	return &state{proxy: func(*url.URL) (*url.URL, error) { return nil, ErrInvalidProxy }, managed: true}
}

func wrap(fn func(*url.URL) (*url.URL, error)) func(*url.URL) (*url.URL, error) {
	return func(u *url.URL) (*url.URL, error) {
		if isLoopback(u.Hostname()) {
			return nil, nil
		}
		return fn(u)
	}
}

func isLoopback(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func proxyForRequest(req *http.Request) (*url.URL, error) {
	if isLoopback(req.URL.Hostname()) {
		return nil, nil
	}
	return load().proxy(req.URL)
}

// directProxyForRequest backs the toggle-off transport. The mode is checked
// per request so a runtime Set switches standalone use to managed mode.
func directProxyForRequest(req *http.Request) (*url.URL, error) {
	if !load().managed {
		return proxyForRequest(req)
	}
	return nil, nil
}

func build() {
	transports.once.Do(func() {
		base := http.DefaultTransport.(*http.Transport)
		transports.proxied = base.Clone()
		transports.proxied.Proxy = proxyForRequest
		transports.direct = base.Clone()
		transports.direct.Proxy = directProxyForRequest
	})
}

// Transport returns the cached transport for the provider toggle. In managed
// mode useProxy selects the app proxy or a strictly direct connection; in
// standalone mode both transports use the environment proxy.
func Transport(useProxy bool) http.RoundTripper {
	build()
	if useProxy {
		return transports.proxied
	}
	return transports.direct
}

// ConnectionTransport returns a private copy of the toggle's transport for a caller that
// manages its own idle connections. It follows the same per-request proxy selection.
func ConnectionTransport(useProxy bool) *http.Transport {
	build()
	if useProxy {
		return transports.proxied.Clone()
	}
	return transports.direct.Clone()
}

// ProxyFor reports the proxy a toggled-on provider would use for target, nil meaning a
// direct connection. It fails rather than falling back while the app proxy is invalid.
func ProxyFor(target *url.URL) (*url.URL, error) {
	return proxyForRequest(&http.Request{URL: target})
}

type ctxKey struct{}

// WithProvider marks ctx with the provider's proxy toggle for clients that
// serve several providers through one ContextTransport.
func WithProvider(ctx context.Context, useProxy bool) context.Context {
	return context.WithValue(ctx, ctxKey{}, useProxy)
}

// ContextTransport routes each request by the toggle stored with
// WithProvider; requests without a mark use the toggle-off transport.
func ContextTransport() http.RoundTripper { return contextTransport{} }

type contextTransport struct{}

func (contextTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	useProxy, _ := req.Context().Value(ctxKey{}).(bool)
	return Transport(useProxy).RoundTrip(req)
}
