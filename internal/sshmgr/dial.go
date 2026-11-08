package sshmgr

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/netproxy"
	"github.com/Stack-Cairn/K-brain/internal/sshhosts"
	"golang.org/x/net/proxy"
)

const dialTimeout = 15 * time.Second

// proxyTarget is a resolved proxy. Errors never include the password.
type proxyTarget struct {
	kind     string // "socks5", "http" or "https"
	address  string // host:port
	username string
	password string
}

// resolveProxy returns the proxy h is reached through, or nil to connect
// directly. useSystemProxy follows K-brain's provider proxy (netproxy): the
// LiveAgent app proxy when LiveAgent manages it, otherwise
// HTTPS_PROXY/HTTP_PROXY/NO_PROXY.
func resolveProxy(h sshhosts.Host) (*proxyTarget, error) {
	p := h.Proxy
	if p.UseSystemProxy {
		u, err := netproxy.ProxyFor(&url.URL{Scheme: "https", Host: h.Address()})
		if err != nil {
			return nil, errors.New("the app proxy is enabled but unusable")
		}
		if u == nil {
			return nil, nil
		}
		kind := strings.ToLower(u.Scheme)
		switch kind {
		case "socks5", "socks5h":
			kind = "socks5"
		case "http", "https":
		default:
			return nil, fmt.Errorf("unsupported app proxy scheme %q", u.Scheme)
		}
		port := u.Port()
		if port == "" {
			port = map[string]string{"socks5": "1080", "http": "80", "https": "443"}[kind]
		}
		target := &proxyTarget{kind: kind, address: net.JoinHostPort(u.Hostname(), port)}
		if u.User != nil {
			target.username = u.User.Username()
			target.password, _ = u.User.Password()
		}
		return target, nil
	}
	if !p.Configured() {
		return nil, nil
	}
	if strings.TrimSpace(p.URL) == "" {
		return nil, errors.New("the SSH proxy host is not set")
	}
	kind, authority, err := sshhosts.ParseProxyURL(p.URL, p.Type)
	if err != nil {
		return nil, err
	}
	host, urlPort := authority, ""
	if h, port, err := net.SplitHostPort(authority); err == nil {
		host, urlPort = h, port
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	port := urlPort
	if p.Port > 0 {
		port = strconv.Itoa(p.Port)
	}
	if port == "" {
		port = map[string]string{"socks5": "1080", "http": "8080"}[kind]
	}
	return &proxyTarget{kind: kind, address: net.JoinHostPort(host, port), username: p.Username, password: p.Password}, nil
}

// dialHost opens the TCP transport to h.
func dialHost(ctx context.Context, h sshhosts.Host) (net.Conn, error) {
	target, err := resolveProxy(h)
	if err != nil {
		return nil, fmt.Errorf("SSH host %q: %v", h.Label(), err)
	}
	ctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	base := &net.Dialer{KeepAlive: 30 * time.Second}
	if target == nil {
		conn, err := base.DialContext(ctx, "tcp", h.Address())
		if err != nil {
			return nil, fmt.Errorf("cannot reach SSH host %q at %s: %v", h.Label(), h.Address(), trimDialError(err))
		}
		return conn, nil
	}
	switch target.kind {
	case "socks5":
		var auth *proxy.Auth
		if target.username != "" || target.password != "" {
			auth = &proxy.Auth{User: target.username, Password: target.password}
		}
		d, err := proxy.SOCKS5("tcp", target.address, auth, base)
		if err != nil {
			return nil, fmt.Errorf("SSH host %q: invalid SOCKS5 proxy", h.Label())
		}
		conn, err := d.(proxy.ContextDialer).DialContext(ctx, "tcp", h.Address())
		if err != nil {
			return nil, fmt.Errorf("cannot reach SSH host %q through the SOCKS5 proxy %s: %v", h.Label(), target.address, trimDialError(err))
		}
		return conn, nil
	default:
		conn, err := base.DialContext(ctx, "tcp", target.address)
		if err != nil {
			return nil, fmt.Errorf("cannot reach the HTTP proxy %s for SSH host %q: %v", target.address, h.Label(), trimDialError(err))
		}
		if target.kind == "https" {
			host, _, _ := net.SplitHostPort(target.address)
			tlsConn := tls.Client(conn, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
			if err := tlsConn.HandshakeContext(ctx); err != nil {
				conn.Close()
				return nil, fmt.Errorf("TLS to the HTTPS proxy %s failed for SSH host %q", target.address, h.Label())
			}
			conn = tlsConn
		}
		tunnel, err := httpConnect(ctx, conn, h.Address(), target)
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("the HTTP proxy %s refused SSH host %q: %v", target.address, h.Label(), err)
		}
		return tunnel, nil
	}
}

// trimDialError drops the "dial tcp" prefix and any proxy URL from err.
func trimDialError(err error) string {
	var op *net.OpError
	if errors.As(err, &op) && op.Err != nil {
		return op.Err.Error()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timed out"
	}
	return err.Error()
}

// httpConnect opens a CONNECT tunnel to address over conn.
func httpConnect(ctx context.Context, conn net.Conn, address string, target *proxyTarget) (net.Conn, error) {
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
		defer conn.SetDeadline(time.Time{})
	}
	req := "CONNECT " + address + " HTTP/1.1\r\nHost: " + address + "\r\n"
	if target.username != "" || target.password != "" {
		req += "Proxy-Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(target.username+":"+target.password)) + "\r\n"
	}
	req += "\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		return nil, errors.New("write failed")
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		return nil, errors.New("invalid proxy response")
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	if reader.Buffered() > 0 {
		return &bufferedConn{Conn: conn, r: reader}, nil
	}
	return conn, nil
}

type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }
