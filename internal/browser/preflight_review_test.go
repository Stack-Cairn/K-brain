package browser

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	chromecdp "github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

// Each fixture owns its browser and profile; discovery uses only its endpoint.
func preflightReviewBrowser(t *testing.T) (context.Context, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("LIVEAGENT_HOME", home)
	t.Setenv("K_BRAIN_HOME", home)
	profile := filepath.Join(home, "profile")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	l := launcher.New().Context(ctx).Bin(chromiumPath(t)).UserDataDir(profile).Headless(true).Leakless(false).Set("remote-debugging-port", "0")
	endpoint, err := l.Launch()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Kill(); l.Cleanup() })
	// Wait for an actual page to be creatable; the DevTools socket can become
	// reachable before Chromium finishes opening its first browser window.
	rb, err := connectLive(ctx, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer detach(rb)
	readyCtx, readyOff := context.WithTimeout(ctx, 5*time.Second)
	defer readyOff()
	for {
		if _, err := (proto.TargetCreateTarget{URL: "about:blank"}).Call(rb.Context(readyCtx)); err == nil {
			break
		}
		select {
		case <-readyCtx.Done():
			t.Fatal("owned Chromium did not finish startup")
		case <-time.After(10 * time.Millisecond):
		}
	}
	return ctx, endpoint
}

func preflightReviewOpen(t *testing.T, ctx context.Context, driver, endpoint, name string) Backend {
	t.Helper()
	t.Setenv("K_BRAIN_CDP_WS", endpoint)
	var b Backend
	var err error
	if driver == DriverRod {
		var concrete *Browser
		concrete, err = openRod(ctx, ModeLive, name)
		if concrete != nil {
			b = concrete
		}
	} else {
		var concrete *chromedpBackend
		concrete, err = openChromedp(ctx, ModeLive, name)
		if concrete != nil {
			b = concrete
		}
	}
	if b != nil {
		t.Cleanup(func() { _ = b.Close() })
	}
	if err != nil {
		t.Fatalf("open %s at %s: %v", driver, endpoint, err)
	}
	return b
}

func TestPreflightReviewInFlightTargetLoss(t *testing.T) {
	for _, driver := range []string{DriverRod, DriverChromedp} {
		for _, fault := range []string{"detach-session", "close-target"} {
			t.Run(driver+"/"+fault, func(t *testing.T) {
				ctx, endpoint := preflightReviewBrowser(t)
				b := preflightReviewOpen(t, ctx, driver, endpoint, "default")
				var effects atomic.Int32
				effected := make(chan struct{}, 1)
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/effect" {
						effects.Add(1)
						effected <- struct{}{}
						fmt.Fprint(w, "done")
						return
					}
					fmt.Fprint(w, "<!doctype html><title>owned in-flight fixture</title>")
				}))
				t.Cleanup(srv.Close)
				if err := b.Navigate(ctx, srv.URL); err != nil {
					t.Fatal(err)
				}
				var interrupt func() error
				switch b := b.(type) {
				case *Browser:
					if fault == "detach-session" {
						id := b.page.SessionID
						interrupt = func() error { return (proto.TargetDetachFromTarget{SessionID: id}).Call(b.browser.Context(ctx)) }
					} else {
						interrupt = func() error {
							_, err := (proto.TargetCloseTarget{TargetID: b.targetID}).Call(b.browser.Context(ctx))
							return err
						}
					}
				case *chromedpBackend:
					browserCtx := chromecdp.WithExecutor(ctx, b.browser)
					if fault == "detach-session" {
						id := chromedp.FromContext(b.targetCtx).Target.SessionID
						interrupt = func() error { return target.DetachFromTarget().WithSessionID(id).Do(browserCtx) }
					} else {
						interrupt = func() error { return target.CloseTarget(b.targetID).Do(browserCtx) }
					}
				}
				disrupted := make(chan error, 1)
				go func() {
					select {
					case <-effected:
						disrupted <- interrupt()
					case <-ctx.Done():
						disrupted <- ctx.Err()
					}
				}()
				s := &Session{name: "default", mode: ModeLive, backend: b, sem: make(chan struct{}, 1)}
				actionCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
				defer cancel()
				calls := 0
				started := time.Now()
				out, err := s.Do(actionCtx, func(backend Backend) (string, error) {
					calls++
					_, err := backend.Eval(actionCtx, `fetch('/effect', {method:'POST'}).then(() => new Promise(() => {}))`)
					return "effect dispatched\n", err
				})
				if disruptionErr := <-disrupted; disruptionErr != nil {
					t.Fatal(disruptionErr)
				}
				t.Logf("elapsed=%v error=%v partial=%q callbacks=%d effects=%d", time.Since(started), err, out, calls, effects.Load())
				if calls != 1 || effects.Load() != 1 || out != "effect dispatched\n" {
					t.Fatal("interruption replayed work or lost partial output")
				}
				if err == nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
					t.Errorf("target loss must interrupt in-flight command with a target/session error before caller deadline; got %v", err)
				}
				if fault == "detach-session" {
					if err := b.Prepare(ctx); err != nil {
						t.Fatalf("recover detached original: %v", err)
					}
					if _, err := b.Eval(ctx, `document.title`); err != nil {
						t.Fatalf("recovered context unusable: %v", err)
					}
				} else if err := b.Prepare(ctx); !errors.Is(err, ErrTargetClosed) {
					t.Errorf("closed target not explicitly classified on next call: %v", err)
				}
			})
		}
	}
}

func TestPreflightReviewChromedpDetachBeforeCall(t *testing.T) {
	ctx, endpoint := preflightReviewBrowser(t)
	b := preflightReviewOpen(t, ctx, DriverChromedp, endpoint, "default").(*chromedpBackend)
	if _, err := b.Eval(ctx, `window.reviewRecoveryMarker = 'original'`); err != nil {
		t.Fatal(err)
	}
	originalTarget := b.targetID
	originalSession := chromedp.FromContext(b.targetCtx).Target.SessionID
	if err := target.DetachFromTarget().WithSessionID(originalSession).Do(chromecdp.WithExecutor(ctx, b.browser)); err != nil {
		t.Fatal(err)
	}
	s := &Session{name: "default", mode: ModeLive, backend: b, sem: make(chan struct{}, 1)}
	callCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	calls := 0
	out, err := s.Do(callCtx, func(backend Backend) (string, error) {
		calls++
		return backend.Eval(callCtx, `window.reviewRecoveryMarker`)
	})
	t.Logf("error=%v output=%q callbacks=%d target=%s original=%s", err, out, calls, b.targetID, originalTarget)
	if err != nil || out != `"original"` || calls != 1 || b.targetID != originalTarget || chromedp.FromContext(b.targetCtx).Target.SessionID == originalSession {
		t.Errorf("detached original target must be reattached before executing callback, preserving its page context")
	}
}

func TestPreflightReviewLiveRegistryEndpointAliases(t *testing.T) {
	for _, drivers := range [][2]string{{DriverRod, DriverRod}, {DriverRod, DriverChromedp}, {DriverChromedp, DriverRod}, {DriverChromedp, DriverChromedp}} {
		for _, alias := range []string{"same-endpoint-control", "localhost", "browser-ws-query"} {
			t.Run(fmt.Sprintf("%s-to-%s/%s", drivers[0], drivers[1], alias), func(t *testing.T) {
				ctx, endpoint := preflightReviewBrowser(t)
				u, err := url.Parse(endpoint)
				if err != nil {
					t.Fatal(err)
				}
				switch alias {
				case "localhost":
					u.Host = "localhost:" + u.Port()
				case "browser-ws-query":
					u.RawQuery = "review=second-session"
				}
				first := preflightReviewOpen(t, ctx, drivers[0], endpoint, "research")
				if err := first.Navigate(ctx, testPage(t)); err != nil {
					t.Fatal(err)
				}
				if _, err := first.Eval(ctx, `window.reviewOwner = 'research'`); err != nil {
					t.Fatal(err)
				}
				second := preflightReviewOpen(t, ctx, drivers[1], u.String(), "default")
				firstID, secondID := leasedTargetID(first), leasedTargetID(second)
				switchErr := second.UseTab(ctx, firstID)
				if firstID == secondID || switchErr == nil {
					if _, err := second.Eval(ctx, `window.reviewOwner = 'overwritten by default'`); err != nil {
						t.Fatal(err)
					}
					observed, err := first.Eval(ctx, `window.reviewOwner`)
					if err != nil {
						t.Fatal(err)
					}
					t.Fatalf("named isolation bypassed: first=%s second=%s UseTab=%v first observes=%s endpoints=%s / %s", firstID, secondID, switchErr, observed, endpoint, u.String())
				}
				if !errors.Is(switchErr, ErrLiveTargetInUse) {
					t.Fatalf("occupied UseTab error = %v", switchErr)
				}
			})
		}
	}
}
