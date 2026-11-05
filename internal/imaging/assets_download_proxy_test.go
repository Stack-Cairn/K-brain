package imaging

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"image"
	pngcodec "image/png"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/netproxy"
)

func pngEncode(w io.Writer, img image.Image) error { return pngcodec.Encode(w, img) }

// connectRecorder is a proxy that records what it is asked to reach and refuses all of it.
func connectRecorder(t *testing.T) (string, <-chan string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	seen := make(chan string, 8)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			request, err := http.ReadRequest(bufio.NewReader(conn))
			if err == nil {
				seen <- request.Method + " " + request.Host + " auth=" + request.Header.Get("Authorization")
			}
			conn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n"))
			conn.Close()
		}
	}()
	return "http://" + listener.Addr().String(), seen
}

func TestOutputDownloadFollowsTheConnectionsProxyToggle(t *testing.T) {
	address, seen := connectRecorder(t)
	if err := netproxy.Set(address); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = netproxy.Set("direct") })
	source := OutputSource{Kind: "url", URL: "https://images.example/result.png", UseProxy: true}

	if _, err := downloadImageOutput(context.Background(), source); err == nil {
		t.Fatal("the refusing proxy should fail the download")
	}
	select {
	case got := <-seen:
		if got != "CONNECT images.example:443 auth=" {
			t.Fatalf("proxy saw %q", got)
		}
	default:
		t.Fatal("a proxied connection downloaded without the proxy")
	}

	// The toggle off stays strictly direct even while an app proxy is configured.
	source.UseProxy = false
	withDirectRoute(t, failingRoute{})
	if _, err := downloadImageOutput(context.Background(), source); err == nil {
		t.Fatal("the failing direct route should fail the download")
	}
	select {
	case got := <-seen:
		t.Fatalf("a direct connection reached the proxy: %q", got)
	default:
	}
}

// failingRoute keeps the direct path off the network: a failure that is not about speed.
type failingRoute struct{}

func (failingRoute) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("no route to host")
}
func (failingRoute) CloseIdleConnections() {}

// crawlingRoute answers 200 and then delivers a body as slowly as the test asks.
type crawlingRoute struct {
	body func(*http.Request) io.ReadCloser
}

func (r crawlingRoute) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"image/png"}}, ContentLength: -1, Body: r.body(req), Request: req}, nil
}
func (crawlingRoute) CloseIdleConnections() {}

// stalledBody blocks until the request is cancelled, like a connection that stopped sending.
func stalledBody(req *http.Request) io.ReadCloser {
	return io.NopCloser(readerFunc(func([]byte) (int, error) {
		<-req.Context().Done()
		return 0, req.Context().Err()
	}))
}

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

func withDirectRoute(t *testing.T, route outputRoute) {
	t.Helper()
	direct := directOutputRoute
	directOutputRoute = func() outputRoute { return route }
	t.Cleanup(func() { directOutputRoute = direct })
}

// The toggle alone decides the route: a crawling direct download never moves to the app proxy,
// it ends with a reason that lets the client suggest the toggle.
func TestSlowDirectDownloadStaysDirectAndNamesTheCause(t *testing.T) {
	address, seen := connectRecorder(t)
	if err := netproxy.Set(address); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = netproxy.Set("direct") })
	withDirectRoute(t, crawlingRoute{body: stalledBody})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err := downloadImageOutput(ctx, OutputSource{Kind: "url", URL: "https://images.example/result.png"})
	if got := outputSaveError(err); got.Code != CodeOutputSaveFailed || got.Reason != reasonSlowDirectDownload {
		t.Fatalf("error = %+v", got)
	}
	var spent downloadBudgetSpent
	if !errors.As(err, &spent) {
		t.Fatalf("a download out of time must not be retried: %v", err)
	}
	select {
	case got := <-seen:
		t.Fatalf("a direct connection reached the proxy: %q", got)
	default:
	}
}

// A broken transfer is not a slow one, and the proxy toggle is not the advice for it.
func TestBrokenDirectDownloadIsNotCalledSlow(t *testing.T) {
	withDirectRoute(t, crawlingRoute{body: func(*http.Request) io.ReadCloser {
		return io.NopCloser(readerFunc(func([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }))
	}})
	_, err := downloadImageOutput(context.Background(), OutputSource{Kind: "url", URL: "https://images.example/result.png"})
	if got := outputSaveError(err); got.Reason != "image download failed" {
		t.Fatalf("error = %+v", got)
	}
}

func TestFastDirectDownloadIsNotInterrupted(t *testing.T) {
	address, seen := connectRecorder(t)
	if err := netproxy.Set(address); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = netproxy.Set("direct") })
	var png bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	if err := pngEncode(&png, img); err != nil {
		t.Fatal(err)
	}
	withDirectRoute(t, crawlingRoute{body: func(*http.Request) io.ReadCloser { return io.NopCloser(bytes.NewReader(png.Bytes())) }})
	content, err := downloadImageOutput(context.Background(), OutputSource{Kind: "url", URL: "https://images.example/result.png"})
	if err != nil || content.Width != 4 {
		t.Fatalf("content = %+v, err = %v", content.Width, err)
	}
	select {
	case got := <-seen:
		t.Fatalf("a fast direct download reached the proxy: %q", got)
	default:
	}
}

func TestProxiedDownloadKeepsURLValidation(t *testing.T) {
	address, seen := connectRecorder(t)
	if err := netproxy.Set(address); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = netproxy.Set("direct") })
	for _, raw := range []string{
		"http://images.example/a.png",
		"https://images.example:8443/a.png",
		"https://127.0.0.1/a.png",
		"https://10.0.0.8/a.png",
		"https://169.254.169.254/latest/meta-data",
		"https://localhost/a.png",
		"https://printer.local/a.png",
		"https://db.internal/a.png",
		"https://user:pass@images.example/a.png",
	} {
		_, err := downloadImageOutput(context.Background(), OutputSource{Kind: "url", URL: raw, UseProxy: true})
		var image *ImageError
		if !errors.As(err, &image) || image.Code != CodeForbidden {
			t.Errorf("%s: err = %v, want forbidden", raw, err)
		}
	}
	select {
	case got := <-seen:
		t.Fatalf("a refused address reached the proxy: %q", got)
	default:
	}
}

func TestInvalidAppProxyNeverFallsBackToDirect(t *testing.T) {
	if err := netproxy.Set("invalid"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = netproxy.Set("direct") })
	_, err := downloadImageOutput(context.Background(), OutputSource{Kind: "url", URL: "https://images.example/a.png", UseProxy: true})
	var image *ImageError
	if !errors.As(err, &image) || image.Code != CodeUnavailable || !strings.Contains(image.Reason, "proxy") {
		t.Fatalf("err = %v", err)
	}
}

func TestProxyErrorsDoNotCarryTheOutputURL(t *testing.T) {
	address, _ := connectRecorder(t)
	if err := netproxy.Set(address); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = netproxy.Set("direct") })
	_, err := downloadImageOutput(context.Background(), OutputSource{Kind: "url", URL: "https://images.example/a.png?sig=PRIVATE", UseProxy: true})
	if err == nil || strings.Contains(outputSaveError(err).Reason, "PRIVATE") {
		t.Fatalf("err = %v, reason = %q", err, outputSaveError(err).Reason)
	}
}
