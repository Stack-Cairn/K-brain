package imaging

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/netproxy"
)

// zz2Recorder is a local CONNECT proxy: it records each target, then runs handle.
type zz2Recorder struct {
	addr string
	mu   sync.Mutex
	seen []string
}

func (p *zz2Recorder) targets() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.seen...)
}

func zz2Proxy(t *testing.T, handle func(net.Conn, *http.Request)) *zz2Recorder {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	p := &zz2Recorder{addr: l.Addr().String()}
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
				p.mu.Lock()
				p.seen = append(p.seen, r.Method+" "+r.Host)
				p.mu.Unlock()
				handle(c, r)
			}()
		}
	}()
	return p
}

func zz2Refuse(c net.Conn, _ *http.Request) {
	c.Write([]byte("HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n"))
}

func zz2UseProxy(t *testing.T, value string) {
	t.Helper()
	if err := netproxy.Set(value); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = netproxy.Set("direct") })
}

func zz2Direct(t *testing.T, route outputRoute) {
	t.Helper()
	old := directOutputRoute
	directOutputRoute = func() outputRoute { return route }
	t.Cleanup(func() { directOutputRoute = old })
}

func zz2PNG(t *testing.T, side int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, side, side))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	img.SetNRGBA(0, 0, color.NRGBA{R: 1, G: 2, B: 3, A: 255})
	var b bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

type zz2Route struct{ body func(*http.Request) io.Reader }

func (r zz2Route) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"image/png"}}, ContentLength: -1, Body: io.NopCloser(r.body(req)), Request: req}, nil
}
func (zz2Route) CloseIdleConnections() {}

type zz2ReadFunc func([]byte) (int, error)

func (f zz2ReadFunc) Read(p []byte) (int, error) { return f(p) }

func zz2Stalled(req *http.Request) io.Reader {
	return zz2ReadFunc(func([]byte) (int, error) {
		<-req.Context().Done()
		return 0, req.Context().Err()
	})
}

const zz2URL = "https://images.zz-review.test/result.png?sig=PRIVATE"

// A direct route that never produces headers is the slowest route there is. It stays direct even
// with an app proxy available, and the failure says what would help.
func TestRegressionHeaderStageStallStaysDirectAndNamesTheCause(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	upstream.StartTLS()
	defer upstream.Close()
	defer close(release)
	route := &http.Transport{
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: true},
		ResponseHeaderTimeout: 200 * time.Millisecond,
		DisableKeepAlives:     true,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
		},
	}
	proxy := zz2Proxy(t, zz2Refuse)
	zz2UseProxy(t, "http://"+proxy.addr)
	zz2Direct(t, route)
	_, err := downloadImageOutput(context.Background(), OutputSource{Kind: "url", URL: zz2URL})
	if got := outputSaveError(err); got.Reason != reasonSlowDirectDownload {
		t.Fatalf("error = %+v", got)
	}
	if got := proxy.targets(); len(got) != 0 {
		t.Fatalf("direct download reached the proxy: %v", got)
	}
	var spent downloadBudgetSpent
	if errors.As(err, &spent) {
		t.Fatal("a stall with time left must stay retryable")
	}
}

// With the toggle already on, suggesting it would be wrong however slow the proxy is.
func TestRegressionSlowProxiedDownloadDoesNotSuggestTheToggle(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	proxy := zz2Proxy(t, func(net.Conn, *http.Request) { <-block })
	zz2UseProxy(t, "http://"+proxy.addr)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := downloadImageOutput(ctx, OutputSource{Kind: "url", URL: zz2URL, UseProxy: true})
	if got := outputSaveError(err); got.Reason != reasonDownloadTimedOut {
		t.Fatalf("error = %+v", got)
	}
}

// Fast enough to clear the floor when the deadline hits: the timeout has another cause.
func TestRegressionFastDownloadThatTimesOutIsNotCalledSlow(t *testing.T) {
	zz2Direct(t, zz2Route{body: func(req *http.Request) io.Reader {
		return io.MultiReader(bytes.NewReader(make([]byte, 1<<20)), zz2Stalled(req))
	}})
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err := downloadImageOutput(ctx, OutputSource{Kind: "url", URL: zz2URL})
	if got := outputSaveError(err); got.Reason != reasonDownloadTimedOut {
		t.Fatalf("error = %+v", got)
	}
}

// The validator rejects a literal private address, but only in the one spelling netip parses.
// Through the proxy nothing resolves the name locally, so each spelling below goes out as a
// CONNECT. The fullwidth ones are worst: net/http itself normalizes them to the real loopback.
func TestRegressionProxiedDownloadRejectsAlternativeAddressSpellings(t *testing.T) {
	for _, host := range []string{
		"2130706433", // decimal 127.0.0.1
		"0x7f.0.0.1", // hex
		"0177.0.0.1", // octal
		"127.1",      // short form
		"0",          // 0.0.0.0
		"2852039166", // decimal 169.254.169.254
		"１２７.０.０.１",  // fullwidth loopback: netproxy's NoProxy catches it, stays direct, refused at dial
		"１０.０.０.８",   // fullwidth private address
		"１６９.２５４.１６９.２５４", // fullwidth metadata address
		"１０。０。０。８",        // ideographic dots
		"127。0。0。1",       // same NoProxy path as fullwidth loopback
		"ｌｏｃａｌｈｏｓｔ",       // same NoProxy path
		"db.ｉｎｔｅｒｎａｌ",     // fullwidth suffix
		"db.internal..",   // second trailing dot
		"localhost..",     // second trailing dot
		"127.0.0.1..",     // second trailing dot
	} {
		t.Run(host, func(t *testing.T) {
			proxy := zz2Proxy(t, zz2Refuse)
			zz2UseProxy(t, "http://"+proxy.addr)
			_, err := downloadImageOutput(context.Background(), OutputSource{Kind: "url", URL: "https://" + host + "/a.png", UseProxy: true})
			if len(proxy.targets()) != 0 {
				t.Fatalf("reached proxy as %v; err=%v", proxy.targets(), err)
			}
		})
	}
}

// Same spellings after a redirect: the provider's CDN chooses the target.
func TestRegressionProxiedRedirectRejectsAlternativeAddressSpelling(t *testing.T) {
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://１２７.０.０.１/a.png")
		w.WriteHeader(http.StatusFound)
	}))
	upstream.StartTLS()
	defer upstream.Close()
	proxy := zz2Proxy(t, func(c net.Conn, r *http.Request) {
		if !strings.HasPrefix(r.Host, "images.") {
			zz2Refuse(c, r)
			return
		}
		u, err := net.Dial("tcp", upstream.Listener.Addr().String())
		if err != nil {
			return
		}
		defer u.Close()
		c.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
		go io.Copy(u, c)
		io.Copy(c, u)
	})
	target, err := url.Parse("http://" + proxy.addr)
	if err != nil {
		t.Fatal(err)
	}
	tr := proxiedImageOutputTransport(target)
	tr.TLSClientConfig.InsecureSkipVerify = true
	defer tr.CloseIdleConnections()
	if _, err = retrieveImageOutput(context.Background(), OutputSource{Kind: "url", URL: zz2URL}, tr); err == nil {
		t.Fatal("expected failure")
	}
	for _, seen := range proxy.targets() {
		if strings.Contains(seen, "127.0.0.1") {
			t.Fatalf("redirect reached the proxy as %q", seen)
		}
	}
}

// ---- PASS: behaviour examined and found correct. ----

func TestRegressionCancelledCallerIsNotCalledSlow(t *testing.T) {
	proxy := zz2Proxy(t, zz2Refuse)
	zz2UseProxy(t, "http://"+proxy.addr)
	zz2Direct(t, zz2Route{body: zz2Stalled})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(30*time.Millisecond, cancel)
	_, err := downloadImageOutput(ctx, OutputSource{Kind: "url", URL: zz2URL})
	if err == nil || slowDirectDownload(err) {
		t.Fatalf("err = %v", err)
	}
	if got := proxy.targets(); len(got) != 0 {
		t.Fatalf("cancelled download went to proxy: %v", got)
	}
}

func TestRegressionAlreadyCancelledCallerDoesNotContactProxy(t *testing.T) {
	proxy := zz2Proxy(t, zz2Refuse)
	zz2UseProxy(t, "http://"+proxy.addr)
	zz2Direct(t, zz2Route{body: zz2Stalled})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := downloadImageOutput(ctx, OutputSource{Kind: "url", URL: zz2URL}); err == nil {
		t.Fatal("expected failure")
	}
	if got := proxy.targets(); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestRegressionSlowReasonCarriesNoAddress(t *testing.T) {
	proxy := zz2Proxy(t, zz2Refuse)
	zz2UseProxy(t, "http://"+proxy.addr)
	zz2Direct(t, zz2Route{body: zz2Stalled})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := downloadImageOutput(ctx, OutputSource{Kind: "url", URL: zz2URL})
	public := outputSaveError(err)
	if public.Code != CodeOutputSaveFailed || strings.Contains(public.Reason, "PRIVATE") || strings.Contains(public.Reason, proxy.addr) {
		t.Fatalf("public=%+v", public)
	}
}

func TestRegressionProxyCredentialsStayOutOfDownloadErrors(t *testing.T) {
	const secret = "zz-s3cret-proxy-pass"
	for _, status := range []string{"502 Bad Gateway", "407 Proxy Authentication Required"} {
		proxy := zz2Proxy(t, func(c net.Conn, _ *http.Request) {
			c.Write([]byte("HTTP/1.1 " + status + "\r\nContent-Length: 0\r\n\r\n"))
		})
		zz2UseProxy(t, "http://user:"+secret+"@"+proxy.addr)
		_, err := downloadImageOutput(context.Background(), OutputSource{Kind: "url", URL: zz2URL, UseProxy: true})
		if err == nil {
			t.Fatal("expected failure")
		}
		public := outputSaveError(err)
		for _, text := range []string{err.Error(), public.Reason, public.Message} {
			if strings.Contains(text, secret) || strings.Contains(text, "PRIVATE") || strings.Contains(text, proxy.addr) {
				t.Fatalf("leak: %q", text)
			}
		}
	}
}

// A download below the speed floor that does finish keeps its billed image.
func TestRegressionSlowDownloadThatFinishesIsKept(t *testing.T) {
	raw := zz2PNG(t, 8)
	proxy := zz2Proxy(t, zz2Refuse)
	zz2UseProxy(t, "http://"+proxy.addr)
	zz2Direct(t, zz2Route{body: func(*http.Request) io.Reader {
		sent := false
		return zz2ReadFunc(func(p []byte) (int, error) {
			if sent {
				return 0, io.EOF
			}
			sent = true
			time.Sleep(100 * time.Millisecond)
			return copy(p, raw), nil
		})
	}})
	if _, err := downloadImageOutput(context.Background(), OutputSource{Kind: "url", URL: zz2URL}); err != nil || len(proxy.targets()) != 0 {
		t.Fatalf("err=%v proxy=%v", err, proxy.targets())
	}
}

// Evidence for the review, not a defect: the toggle-off real route resolves the image host, which
// is the address pinning. TestOutputDownloadFollowsTheConnectionsProxyToggle reaches this branch
// with net.DefaultResolver intact, so that test sends real DNS queries for images.example. The
// resolver here is local and refuses, so nothing leaves the machine.
func TestRegressionToggleOffRealRouteQueriesDNS(t *testing.T) {
	var mu sync.Mutex
	queried := 0
	old := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(context.Context, string, string) (net.Conn, error) {
		mu.Lock()
		queried++
		mu.Unlock()
		return nil, errors.New("blocked in test")
	}}
	t.Cleanup(func() { net.DefaultResolver = old })
	proxy := zz2Proxy(t, zz2Refuse)
	zz2UseProxy(t, "http://"+proxy.addr)
	_, _ = downloadImageOutput(context.Background(), OutputSource{Kind: "url", URL: "https://images.example/result.png"})
	mu.Lock()
	defer mu.Unlock()
	if queried == 0 {
		t.Fatal("toggle-off route no longer resolves the host, so it no longer pins addresses")
	}
	t.Logf("%d resolver dials for images.example", queried)
}
