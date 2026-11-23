package netproxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const target = "http://provider.test/x"

// fakeProxy answers absolute-URI requests as an HTTP forward proxy would.
func fakeProxy(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.IsAbs() && r.URL.Host == "provider.test" {
			hits.Add(1)
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func get(t *testing.T, useProxy bool, rawURL string) (*http.Response, error) {
	t.Helper()
	client := &http.Client{Transport: Transport(useProxy), Timeout: 10 * time.Second}
	resp, err := client.Get(rawURL)
	if err == nil {
		resp.Body.Close()
	}
	return resp, err
}

func TestProxiedTransportUsesConfiguredProxy(t *testing.T) {
	proxy, hits := fakeProxy(t)
	if err := Set(proxy.URL); err != nil {
		t.Fatal(err)
	}
	resp, err := get(t, true, target)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || hits.Load() != 1 {
		t.Fatalf("status %d, proxy hits %d", resp.StatusCode, hits.Load())
	}
}

func TestDirectTransportIgnoresProxyAndEnvironment(t *testing.T) {
	proxy, hits := fakeProxy(t)
	setEnvProxy(t, proxy.URL, proxy.URL, "")
	if err := Set(proxy.URL); err != nil {
		t.Fatal(err)
	}
	if _, err := get(t, false, target); err == nil {
		t.Fatal("expected DNS failure for direct request")
	}
	if hits.Load() != 0 {
		t.Fatalf("direct transport hit proxy %d times", hits.Load())
	}
}

func TestSetDirectDisablesProxy(t *testing.T) {
	_, hits := fakeProxy(t)
	if err := Set("direct"); err != nil {
		t.Fatal(err)
	}
	if _, err := get(t, true, target); err == nil {
		t.Fatal("expected DNS failure for direct request")
	}
	if hits.Load() != 0 {
		t.Fatalf("proxy hit %d times", hits.Load())
	}
}

func TestInvalidProxyFailsProxiedRequests(t *testing.T) {
	for _, value := range []string{"invalid", "ftp://127.0.0.1:21", "http://127.0.0.1", "://bad", "127.0.0.1:8080"} {
		err := Set(value)
		if value != "invalid" && !errors.Is(err, ErrInvalidProxy) {
			t.Fatalf("Set(%q) = %v, want ErrInvalidProxy", value, err)
		}
		if _, err := get(t, true, target); !errors.Is(err, ErrInvalidProxy) {
			t.Fatalf("Set(%q): request error %v, want ErrInvalidProxy", value, err)
		}
	}
}

func TestLoopbackBypassesProxy(t *testing.T) {
	proxy, hits := fakeProxy(t)
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer local.Close()
	if err := Set(proxy.URL); err != nil {
		t.Fatal(err)
	}
	resp, err := get(t, true, local.URL)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNoContent || hits.Load() != 0 {
		t.Fatalf("status %d, proxy hits %d", resp.StatusCode, hits.Load())
	}
}

func TestSocks5hAccepted(t *testing.T) {
	if err := Set("socks5h://127.0.0.1:1080"); err != nil {
		t.Fatal(err)
	}
	if err := Set("socks5://127.0.0.1:1080"); err != nil {
		t.Fatal(err)
	}
}

func TestErrorsDoNotLeakCredentials(t *testing.T) {
	err := Set("http://user:s3cret@host")
	if err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("Set error %v", err)
	}
	if _, err := get(t, true, target); err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("request error %v", err)
	}
	if err := Set("http://user:s3cret@127.0.0.1:1"); err != nil {
		t.Fatal(err)
	}
	if _, err := get(t, true, target); err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("request error %v", err)
	}
}

func TestContextTransportRoutesByProvider(t *testing.T) {
	proxy, hits := fakeProxy(t)
	if err := Set(proxy.URL); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: ContextTransport(), Timeout: 10 * time.Second}
	for _, useProxy := range []bool{false, true} {
		req, _ := http.NewRequestWithContext(WithProvider(context.Background(), useProxy), http.MethodGet, target, nil)
		if resp, err := client.Do(req); err == nil {
			resp.Body.Close()
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("proxy hits %d, want 1", hits.Load())
	}
}

func TestRejectsUnusableProxyAddresses(t *testing.T) {
	for _, value := range []string{"http://:8080", "http://proxy.test:0", "http://proxy.test:65536", "http://proxy.test:8080/path", "http://proxy.test:8080?token=secret", "http://proxy.test:8080#fragment"} {
		t.Run(value, func(t *testing.T) {
			if err := Set(value); !errors.Is(err, ErrInvalidProxy) {
				t.Fatalf("expected invalid proxy, got %v", err)
			}
		})
	}
}

func TestConcurrentFirstSetAndLoad(t *testing.T) {
	defer Set("direct")
	for range 5000 {
		initOnce = sync.Once{}
		current.Store(nil)
		var wg sync.WaitGroup
		wg.Add(2)
		start := make(chan struct{})
		go func() { defer wg.Done(); <-start; _ = Set("direct") }()
		go func() {
			defer wg.Done()
			<-start
			if load() == nil {
				t.Error("proxy state was nil after initialization")
			}
		}()
		close(start)
		wg.Wait()
	}
}

func TestExistingClientUsesUpdatedProxy(t *testing.T) {
	defer Set("direct")
	first, firstHits := fakeProxy(t)
	second, secondHits := fakeProxy(t)
	client := &http.Client{Transport: Transport(true), Timeout: time.Second}
	for _, value := range []string{first.URL, second.URL, "invalid", first.URL} {
		if err := Set(value); err != nil {
			t.Fatal(err)
		}
		resp, err := client.Get(target)
		if value == "invalid" {
			if !errors.Is(err, ErrInvalidProxy) {
				t.Fatalf("expected fail-closed request, got %v", err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	if firstHits.Load() != 2 || secondHits.Load() != 1 {
		t.Fatalf("unexpected proxy hits: %d / %d", firstHits.Load(), secondHits.Load())
	}
	if err := Set("direct"); err != nil {
		t.Fatal(err)
	}
	req := &http.Request{URL: &url.URL{Scheme: "https", Host: "provider.test"}}
	if proxy, err := proxyForRequest(req); proxy != nil || err != nil {
		t.Fatalf("direct route = %v, %v", proxy, err)
	}
}

// connectProxy counts CONNECT tunnels to provider.test, as HTTPS requests use.
func connectProxy(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect && r.Host == "provider.test:443" {
			hits.Add(1)
		}
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestStandaloneBothTogglesUseEnvironmentProxy(t *testing.T) {
	httpProxy, httpHits := fakeProxy(t)
	httpsProxy, httpsHits := connectProxy(t)
	setEnvProxy(t, httpProxy.URL, httpsProxy.URL, "")
	standaloneForTest(t)
	for _, useProxy := range []bool{false, true} {
		resp, err := get(t, useProxy, target)
		if err != nil {
			t.Fatalf("useProxy=%v: %v", useProxy, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("useProxy=%v: status %d", useProxy, resp.StatusCode)
		}
		if _, err := get(t, useProxy, "https://provider.test/x"); err == nil {
			t.Fatalf("useProxy=%v: expected CONNECT to be refused", useProxy)
		}
	}
	if httpHits.Load() != 2 || httpsHits.Load() != 2 {
		t.Fatalf("env proxy hits http=%d https=%d, want 2/2", httpHits.Load(), httpsHits.Load())
	}
}

func TestStandaloneRespectsNoProxy(t *testing.T) {
	proxy, hits := fakeProxy(t)
	setEnvProxy(t, proxy.URL, proxy.URL, "provider.test")
	standaloneForTest(t)
	for _, useProxy := range []bool{false, true} {
		if _, err := get(t, useProxy, target); err == nil {
			t.Fatalf("useProxy=%v: expected DNS failure for NO_PROXY host", useProxy)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("NO_PROXY host hit proxy %d times", hits.Load())
	}
}

func TestManagedEnvDirectIgnoresEnvironmentProxy(t *testing.T) {
	envProxy, envHits := fakeProxy(t)
	appProxy, appHits := fakeProxy(t)
	setEnvProxy(t, envProxy.URL, envProxy.URL, "")
	for _, value := range []string{"direct", appProxy.URL} {
		t.Run(value, func(t *testing.T) {
			t.Setenv(EnvVar, value)
			resetForTest(t)
			if _, err := get(t, false, target); err == nil {
				t.Fatal("expected DNS failure for direct request")
			}
		})
	}
	if envHits.Load() != 0 || appHits.Load() != 0 {
		t.Fatalf("toggle-off hit proxies env=%d app=%d", envHits.Load(), appHits.Load())
	}
}

func TestSetSwitchesStandaloneToManaged(t *testing.T) {
	envProxy, envHits := fakeProxy(t)
	appProxy, appHits := fakeProxy(t)
	setEnvProxy(t, envProxy.URL, envProxy.URL, "")
	standaloneForTest(t)
	direct := &http.Client{Transport: Transport(false), Timeout: 10 * time.Second}
	proxied := &http.Client{Transport: Transport(true), Timeout: 10 * time.Second}
	for _, c := range []*http.Client{direct, proxied} {
		resp, err := c.Get(target)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	if err := Set(appProxy.URL); err != nil {
		t.Fatal(err)
	}
	if _, err := direct.Get(target); err == nil {
		t.Fatal("expected DNS failure for managed direct request")
	}
	resp, err := proxied.Get(target)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if envHits.Load() != 2 || appHits.Load() != 1 {
		t.Fatalf("proxy hits env=%d app=%d, want 2/1", envHits.Load(), appHits.Load())
	}
}
