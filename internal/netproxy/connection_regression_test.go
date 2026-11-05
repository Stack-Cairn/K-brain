package netproxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"testing"
)

func zz2ProxyOf(t *testing.T, tr *http.Transport, raw string) *url.URL {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := tr.Proxy(req)
	if err != nil {
		t.Fatalf("proxy: %v", err)
	}
	return got
}

// An already-built connection transport follows a later Set, in both directions.
func TestRegressionConnectionTransportFollowsRuntimeSet(t *testing.T) {
	resetForTest(t)
	if err := Set("http://127.0.0.1:1111"); err != nil {
		t.Fatal(err)
	}
	proxied, direct := ConnectionTransport(true), ConnectionTransport(false)
	const target = "https://provider.zz-review.test/v1"
	if got := zz2ProxyOf(t, proxied, target); got == nil || got.Host != "127.0.0.1:1111" {
		t.Fatalf("got %v", got)
	}
	if got := zz2ProxyOf(t, direct, target); got != nil {
		t.Fatalf("toggle off used %v", got)
	}
	if err := Set("http://127.0.0.1:2222"); err != nil {
		t.Fatal(err)
	}
	if got := zz2ProxyOf(t, proxied, target); got == nil || got.Host != "127.0.0.1:2222" {
		t.Fatalf("after Set: %v", got)
	}
	if err := Set("direct"); err != nil {
		t.Fatal(err)
	}
	if got := zz2ProxyOf(t, proxied, target); got != nil {
		t.Fatalf("after direct: %v", got)
	}
}

// Invalid fails the request before any connection, never falling back to direct.
func TestRegressionConnectionTransportInvalidFailsClosed(t *testing.T) {
	resetForTest(t)
	if err := Set("invalid"); err != nil {
		t.Fatal(err)
	}
	dialed := false
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{
		ConnectStart: func(string, string) { dialed = true },
		DNSStart:     func(httptrace.DNSStartInfo) { dialed = true },
	}), http.MethodGet, "https://provider.zz-review.test/v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = (&http.Client{Transport: ConnectionTransport(true)}).Do(req)
	if !errors.Is(err, ErrInvalidProxy) || dialed {
		t.Fatalf("err=%v dialed=%v", err, dialed)
	}
	u, _ := url.Parse("https://provider.zz-review.test/v1")
	if _, err := ProxyFor(u); !errors.Is(err, ErrInvalidProxy) {
		t.Fatalf("ProxyFor err=%v", err)
	}
}

// Closing a connection's sockets leaves the shared chat transport's pool alone.
func TestRegressionConnectionTransportIdleCloseIsPrivate(t *testing.T) {
	resetForTest(t)
	if err := Set("direct"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok") }))
	defer srv.Close()
	shared := &http.Client{Transport: Transport(true)}
	fetch := func(client *http.Client) (reused bool) {
		t.Helper()
		req, err := http.NewRequestWithContext(httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{
			GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused },
		}), http.MethodGet, srv.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return reused
	}
	fetch(shared)
	private := ConnectionTransport(true)
	privateClient := &http.Client{Transport: private}
	if fetch(privateClient) {
		t.Fatal("private transport reused the shared pool")
	}
	private.CloseIdleConnections()
	if !fetch(shared) {
		t.Fatal("closing a private transport closed the shared pool")
	}
	if fetch(privateClient) {
		t.Fatal("private pool survived its own close")
	}
	private.CloseIdleConnections()
}
