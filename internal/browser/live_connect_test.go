package browser

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod"
)

// pendingApprovalServer accepts TCP and reads the handshake but never answers,
// like Chrome while its "Allow remote debugging?" prompt is open.
func pendingApprovalServer(t *testing.T) (string, <-chan struct{}) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		buf := make([]byte, 4096)
		for {
			if _, err := conn.Read(buf); err != nil {
				close(closed)
				return
			}
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return fmt.Sprintf("ws://%s/devtools/browser/pending", ln.Addr()), closed
}

func TestConnectLiveWaitsBeyondOldProbeWindow(t *testing.T) {
	ws, closed := pendingApprovalServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := connectLive(ctx, ws)
	elapsed := time.Since(start)
	if !errors.Is(err, ErrPermissionBlocked) {
		t.Fatalf("pending approval must report permission-blocked, got %v", err)
	}
	if !strings.Contains(err.Error(), "Allow") {
		t.Fatalf("error should tell the user to click Allow: %v", err)
	}
	if elapsed < 1400*time.Millisecond {
		t.Fatalf("gave up after %v; must wait for the whole call budget, not a short probe", elapsed)
	}
	if elapsed > 4*time.Second {
		t.Fatalf("did not stop when ctx ended: %v", elapsed)
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("pending connection was not closed after giving up")
	}
}

func TestConnectLiveCancellationRemainsCancellation(t *testing.T) {
	ws, _ := pendingApprovalServer(t)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { _, err := connectLive(ctx, ws); done <- err }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "timed out") {
			t.Fatalf("cancel was reported as timeout: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("pending approval ignored cancellation")
	}
}

func TestConnectLiveRejectsForbiddenHandshake(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})}
	go srv.Serve(ln)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = connectLive(ctx, fmt.Sprintf("ws://%s/devtools/browser/x", ln.Addr()))
	if !errors.Is(err, ErrPermissionBlocked) {
		t.Fatalf("403 handshake must report permission-blocked, got %v", err)
	}
}

func TestConnectLiveBrowserSkipsRefusedCandidates(t *testing.T) {
	noEnvCDP(t)
	t.Setenv("K_BRAIN_CDP_WS", "ws://first/devtools/browser/a")
	orig := connectLive
	t.Cleanup(func() { connectLive = orig })

	var tried []string
	want := rod.New()
	connectLive = func(ctx context.Context, ws string) (*rod.Browser, error) {
		tried = append(tried, ws)
		return want, nil
	}
	got, endpoint, err := connectLiveBrowser(context.Background())
	if err != nil || got != want || endpoint != "ws://first/devtools/browser/a" {
		t.Fatalf("got %v %v", got, err)
	}

	connectLive = func(ctx context.Context, ws string) (*rod.Browser, error) {
		return nil, fmt.Errorf("%w: 404", errLiveRefused)
	}
	if _, _, err := connectLiveBrowser(context.Background()); !errors.Is(err, ErrNoLiveBrowser) {
		t.Fatalf("all refused must fall back as no-live-browser, got %v", err)
	}

	connectLive = func(ctx context.Context, ws string) (*rod.Browser, error) {
		return nil, fmt.Errorf("%w: waiting", ErrPermissionBlocked)
	}
	if _, _, err := connectLiveBrowser(context.Background()); !errors.Is(err, ErrPermissionBlocked) || errors.Is(err, ErrNoLiveBrowser) {
		t.Fatalf("pending approval must not fall back to the dedicated browser, got %v", err)
	}
}
