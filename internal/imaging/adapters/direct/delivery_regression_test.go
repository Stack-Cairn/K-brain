package direct

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/imaging"
	"github.com/Stack-Cairn/K-brain/internal/netproxy"
)

// zz2Proxy is a real CONNECT proxy on a local socket. Every test target host is fake: the proxy
// decides where the tunnel goes, so no request leaves the machine.
type zz2Proxy struct {
	addr     string
	connects atomic.Int64
}

func zz2StartProxy(t *testing.T, handle func(client net.Conn, r *http.Request)) *zz2Proxy {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	p := &zz2Proxy{addr: l.Addr().String()}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				r, err := http.ReadRequest(bufio.NewReader(c))
				if err != nil {
					return
				}
				p.connects.Add(1)
				handle(c, r)
			}()
		}
	}()
	return p
}

// zz2Tunnel answers 200 to CONNECT and splices to upstream.
func zz2Tunnel(upstream string) func(net.Conn, *http.Request) {
	return func(c net.Conn, _ *http.Request) {
		u, err := net.Dial("tcp", upstream)
		if err != nil {
			c.Write([]byte("HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n"))
			return
		}
		defer u.Close()
		c.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
		done := make(chan struct{}, 2)
		go func() { io.Copy(u, c); done <- struct{}{} }()
		go func() { io.Copy(c, u); done <- struct{}{} }()
		<-done
	}
}

// zz2Client uses the production transport constructor; only certificate trust is relaxed,
// because the upstream is a local httptest certificate.
func zz2Client(t *testing.T, proxyValue string) *http.Client {
	t.Helper()
	_ = netproxy.Set(proxyValue)
	t.Cleanup(func() { _ = netproxy.Set("direct") })
	tr := netproxy.ConnectionTransport(true)
	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

const zz2Base = "https://provider.zz-review.test"

func zz2Submit(t *testing.T, ctx context.Context, client *http.Client) *imaging.AdapterError {
	t.Helper()
	a := adapter(t, OpenAIID, options(client, zz2Base))
	_, err := a.Submit(ctx, request(OpenAIID))
	var ae *imaging.AdapterError
	if !errors.As(err, &ae) {
		t.Fatalf("err = %v, want AdapterError", err)
	}
	return ae
}

// ---- Delivered, or possibly delivered: must stay uncertain. These pass and pin safety. ----

func TestRegressionDeliveredThroughTunnelThenDroppedStaysUncertain(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		name := "http1"
		if h2 {
			name = "http2"
		}
		t.Run(name, func(t *testing.T) {
			var delivered atomic.Int64
			release := make(chan struct{})
			srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				delivered.Add(1)
				<-release
				panic(http.ErrAbortHandler)
			}))
			srv.EnableHTTP2 = h2
			srv.StartTLS()
			defer srv.Close()
			defer func() { recover() }()
			proxy := zz2StartProxy(t, zz2Tunnel(srv.Listener.Addr().String()))
			client := zz2Client(t, "http://"+proxy.addr)
			go func() {
				for delivered.Load() == 0 {
					time.Sleep(time.Millisecond)
				}
				close(release)
			}()
			ae := zz2Submit(t, context.Background(), client)
			if delivered.Load() != 1 || ae.Delivery != imaging.DeliveryUncertain || ae.Code != imaging.CodeResultUnknown {
				t.Fatalf("delivered=%d delivery=%s code=%s", delivered.Load(), ae.Delivery, ae.Code)
			}
		})
	}
}

// Drops the tunnel while the upstream has seen only part of the body.
func TestRegressionTunnelDroppedMidBodyStaysUncertain(t *testing.T) {
	var sawHeaders atomic.Bool
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawHeaders.Store(true)
		panic(http.ErrAbortHandler)
	}))
	upstream.EnableHTTP2 = false
	upstream.StartTLS()
	defer upstream.Close()
	proxy := zz2StartProxy(t, zz2Tunnel(upstream.Listener.Addr().String()))
	ae := zz2Submit(t, context.Background(), zz2Client(t, "http://"+proxy.addr))
	if !sawHeaders.Load() || ae.Delivery != imaging.DeliveryUncertain {
		t.Fatalf("sawHeaders=%v delivery=%s", sawHeaders.Load(), ae.Delivery)
	}
}

// Cancelling after the upstream has the request must not read as "never sent".
func TestRegressionCancelAfterDeliveryThroughTunnelStaysUncertain(t *testing.T) {
	got := make(chan struct{}, 1)
	release := make(chan struct{})
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		got <- struct{}{}
		<-release
	}))
	upstream.StartTLS()
	defer upstream.Close()
	defer close(release)
	proxy := zz2StartProxy(t, zz2Tunnel(upstream.Listener.Addr().String()))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { <-got; cancel() }()
	ae := zz2Submit(t, ctx, zz2Client(t, "http://"+proxy.addr))
	if ae.Delivery != imaging.DeliveryUncertain {
		t.Fatalf("delivery=%s", ae.Delivery)
	}
}

// A proxy may put anything in its status text. A delivered request must not be flipped to
// not_sent by a body or header phrase; only the pre-send path may match the string.
func TestRegressionResponseTextCannotFlipDeliveredRequest(t *testing.T) {
	var delivered atomic.Int64
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		delivered.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		conn.Write([]byte("HTTP/1.1 TLS handshake timeout\r\n\r\n"))
		conn.Close()
	}))
	upstream.EnableHTTP2 = false
	upstream.StartTLS()
	defer upstream.Close()
	proxy := zz2StartProxy(t, zz2Tunnel(upstream.Listener.Addr().String()))
	ae := zz2Submit(t, context.Background(), zz2Client(t, "http://"+proxy.addr))
	if delivered.Load() != 1 || ae.Delivery == imaging.DeliveryNotSent {
		t.Fatalf("delivered=%d delivery=%s", delivered.Load(), ae.Delivery)
	}
}

// ---- Provably unsent: classified not_sent. ----

// The tunnel is up, the upstream never finishes TLS: no HTTP byte can have reached it.
func TestRegressionTLSTimeoutInsideTunnelIsNotSent(t *testing.T) {
	var received atomic.Int64
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	go func() {
		for {
			c, err := upstream.Accept()
			if err != nil {
				return
			}
			go func() {
				n, _ := io.Copy(io.Discard, c)
				received.Add(n)
			}()
		}
	}()
	proxy := zz2StartProxy(t, zz2Tunnel(upstream.Addr().String()))
	client := zz2Client(t, "http://"+proxy.addr)
	client.Transport.(*http.Transport).TLSHandshakeTimeout = 200 * time.Millisecond
	ae := zz2Submit(t, context.Background(), client)
	if ae.Delivery != imaging.DeliveryNotSent {
		t.Fatalf("delivery=%s", ae.Delivery)
	}
}

// ---- Provably unsent, classified uncertain: expected to FAIL on the reviewed tree. ----

// The configured proxy is not listening: the same failure failedBeforeSending exists to catch
// on a direct route. net/http wraps it as Op "proxyconnect" (or "socks connect"), and errors.As
// stops at that outer OpError, so the Op == "dial" test never sees the inner dial.
func TestRegressionProxyDownIsNotSent(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	for _, scheme := range []string{"http", "https", "socks5", "socks5h"} {
		t.Run(scheme, func(t *testing.T) {
			ae := zz2Submit(t, context.Background(), zz2Client(t, scheme+"://"+addr))
			if ae.Delivery != imaging.DeliveryNotSent {
				t.Fatalf("delivery=%s code=%s reason=%q", ae.Delivery, ae.Code, ae.Reason)
			}
		})
	}
}

// The proxy refuses the tunnel, so only CONNECT existed and the prompt went nowhere. The job is
// still reported as "result unknown" with no retry, as if it might have been billed.
func TestRegressionProxyRefusingTunnelIsNotSent(t *testing.T) {
	for _, status := range []string{"502 Bad Gateway", "407 Proxy Authentication Required", "503 Service Unavailable"} {
		t.Run(status, func(t *testing.T) {
			proxy := zz2StartProxy(t, func(c net.Conn, _ *http.Request) {
				c.Write([]byte("HTTP/1.1 " + status + "\r\nContent-Length: 0\r\n\r\n"))
			})
			ae := zz2Submit(t, context.Background(), zz2Client(t, "http://"+proxy.addr))
			if proxy.connects.Load() == 0 {
				t.Fatal("proxy was not used")
			}
			if ae.Delivery != imaging.DeliveryNotSent {
				t.Fatalf("nothing but CONNECT was sent: delivery=%s code=%s reason=%q", ae.Delivery, ae.Code, ae.Reason)
			}
		})
	}
}

// The proxy accepts the tunnel and hangs up before TLS: same conclusion, no HTTP was written.
func TestRegressionTunnelClosedBeforeTLSIsNotSent(t *testing.T) {
	proxy := zz2StartProxy(t, func(c net.Conn, _ *http.Request) {
		c.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
	})
	ae := zz2Submit(t, context.Background(), zz2Client(t, "http://"+proxy.addr))
	if ae.Delivery != imaging.DeliveryNotSent {
		t.Fatalf("no HTTP was written: delivery=%s code=%s reason=%q", ae.Delivery, ae.Code, ae.Reason)
	}
}

// netproxy never dials while the app proxy is invalid, and says so. The adapter turns that
// into "result unknown" and a reason that blames the provider.
func TestRegressionInvalidAppProxyIsReportedAsNotSent(t *testing.T) {
	ae := zz2Submit(t, context.Background(), zz2Client(t, "invalid"))
	if ae.Delivery != imaging.DeliveryNotSent {
		t.Fatalf("delivery=%s code=%s reason=%q", ae.Delivery, ae.Code, ae.Reason)
	}
	if !strings.Contains(ae.Reason, "proxy") {
		t.Fatalf("reason %q does not name the proxy", ae.Reason)
	}
}

// ---- Credentials in the proxy URL never reach a public field. ----

func TestRegressionProxyCredentialsStayOutOfErrors(t *testing.T) {
	const secret = "zz-s3cret-proxy-pass"
	for name, status := range map[string]string{"refused": "502 Bad Gateway", "auth": "407 Proxy Authentication Required"} {
		t.Run(name, func(t *testing.T) {
			var sawAuth atomic.Bool
			proxy := zz2StartProxy(t, func(c net.Conn, r *http.Request) {
				sawAuth.Store(r.Header.Get("Proxy-Authorization") != "")
				c.Write([]byte("HTTP/1.1 " + status + "\r\nContent-Length: 0\r\n\r\n"))
			})
			ae := zz2Submit(t, context.Background(), zz2Client(t, "http://user:"+secret+"@"+proxy.addr))
			if !sawAuth.Load() {
				t.Fatal("credentials not used")
			}
			public := imaging.PublicError(&imaging.ImageError{Code: ae.Code, Reason: ae.Reason})
			if strings.Contains(public.Reason, secret) || strings.Contains(ae.Error(), secret) || strings.Contains(public.Reason, proxy.addr) {
				t.Fatalf("leak: reason=%q err=%q", public.Reason, ae.Error())
			}
		})
	}
}
