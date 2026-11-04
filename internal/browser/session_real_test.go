package browser

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-rod/rod/lib/cdp"
	"github.com/go-rod/rod/lib/proto"
)

// These regressions use only a newly launched headless browser and temporary
// profile. They do not discover or attach to a user's existing live browser.
func realSession(t *testing.T, firstCtx context.Context) (*Session, *Browser) {
	t.Helper()
	t.Setenv("ROD_BROWSER_BIN", chromiumPath(t))
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	t.Setenv("LIVEAGENT_HOME", home)
	t.Setenv("K_BRAIN_HOME", home)
	b, err := openRod(firstCtx, ModeHeadless, "real-session-test")
	if err != nil {
		t.Fatalf("launch isolated headless browser: %v", err)
	}
	t.Cleanup(func() {
		if err := b.Close(); err != nil {
			t.Errorf("close test-owned headless browser: %v", err)
		}
	})
	if b.obtained != ObtainedLaunched {
		t.Fatalf("expected newly launched browser, got %v", b.obtained)
	}
	rel, err := filepath.Rel(home, b.profileDir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Fatalf("test profile escaped temporary home: %q (%v)", b.profileDir, err)
	}
	// Inject the concrete, real backend without replacing package-global open
	// hooks or depending on the process-wide selected browser driver.
	return &Session{name: "real-session-test", mode: ModeHeadless, sem: make(chan struct{}, 1), backend: b}, b
}

func realSessionCounter(t *testing.T) (string, *atomic.Int32) {
	t.Helper()
	count := new(atomic.Int32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, `<!doctype html><title>isolated recovery test</title><p id="marker">original document</p>`)
		case "/effect":
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			fmt.Fprint(w, count.Add(1))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL, count
}

func realSessionNavigate(t *testing.T, s *Session, ctx context.Context, url string) {
	t.Helper()
	if _, err := s.Do(ctx, func(b Backend) (string, error) {
		return "", b.Navigate(ctx, url)
	}); err != nil {
		t.Fatalf("navigate test page: %v", err)
	}
}

func realSessionRead(t *testing.T, s *Session, ctx context.Context, expression, want string) {
	t.Helper()
	got, err := s.Do(ctx, func(b Backend) (string, error) { return b.Eval(ctx, expression) })
	if err != nil || got != want {
		t.Fatalf("read %s: got %q, want %q, error %v", expression, got, want, err)
	}
}

func TestRealSessionReattachesOriginalTargetAfterDetach(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	s, b := realSession(t, ctx)
	url, count := realSessionCounter(t)
	realSessionNavigate(t, s, ctx, url)
	realSessionRead(t, s, ctx, `fetch('/effect', {method:'POST'}).then(r => r.text())`, `"1"`)
	realSessionRead(t, s, ctx, `window.recoveryMarker = 'kept in original target'`, `"kept in original target"`)
	originalTarget, oldSession := b.targetID, b.page.SessionID
	if err := (proto.TargetDetachFromTarget{SessionID: oldSession}).Call(b.browser.Context(ctx)); err != nil {
		t.Fatalf("detach test page session: %v", err)
	}
	// Do must let Prepare repair the invalid CDP session before running fn.
	realSessionRead(t, s, ctx, `window.recoveryMarker`, `"kept in original target"`)
	if b.targetID != originalTarget || b.page.TargetID != originalTarget || b.page.SessionID == oldSession {
		t.Fatalf("wrong recovery: target=%s page=%s session=%s (original=%s session=%s)", b.targetID, b.page.TargetID, b.page.SessionID, originalTarget, oldSession)
	}
	if got := count.Load(); got != 1 {
		t.Fatalf("effect count after recovery = %d, want 1", got)
	}
}

func TestRealSessionInterruptedActionsAreNotReplayed(t *testing.T) {
	for _, fault := range []string{"detach-session", "close-connection"} {
		t.Run(fault, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
			defer cancel()
			s, b := realSession(t, ctx)
			url, count := realSessionCounter(t)
			realSessionNavigate(t, s, ctx, url)
			originalTarget, endpoint := b.targetID, b.endpoint
			calls := 0
			var protocolErr error
			out, err := s.Do(ctx, func(backend Backend) (string, error) {
				calls++
				value, err := backend.Eval(ctx, `fetch('/effect', {method:'POST'}).then(r => r.text())`)
				if err != nil {
					return "", err
				}
				if value != `"1"` {
					t.Fatalf("unexpected first effect: %s", value)
				}
				if fault == "detach-session" {
					if err := (proto.TargetDetachFromTarget{SessionID: b.page.SessionID}).Call(b.browser.Context(ctx)); err != nil {
						t.Fatalf("detach test page session: %v", err)
					}
				} else if !detach(b.browser) {
					t.Fatal("could not close this test's CDP connection")
				}
				// A real command on the now invalid session/connection supplies the
				// error; no fabricated EOF or protocol error is passed to Do.
				_, protocolErr = (proto.PageGetFrameTree{}).Call(b.page.Context(ctx))
				return "effect completed: " + value + "\n", protocolErr
			})
			if protocolErr == nil || err == nil || !errors.Is(err, protocolErr) {
				t.Fatalf("missing real interrupted-command error: protocol=%v Do=%v", protocolErr, err)
			}
			if fault == "detach-session" {
				var cdpErr *cdp.Error
				if !errors.As(protocolErr, &cdpErr) || cdpErr.Code != -32001 {
					t.Fatalf("expected real session error, got %T: %v", protocolErr, protocolErr)
				}
			}
			if fault == "close-connection" && !isConnErr(protocolErr) {
				t.Fatalf("expected real connection error, got %T: %v", protocolErr, protocolErr)
			}
			t.Logf("actual %s error: %T: %v", fault, protocolErr, protocolErr)
			if out != "effect completed: \"1\"\n" || !strings.Contains(err.Error(), "not replayed") {
				t.Fatalf("interruption lost partial output or replay warning: out=%q err=%v", out, err)
			}
			if calls != 1 || count.Load() != 1 {
				t.Fatalf("interrupted callback replayed: calls=%d effects=%d", calls, count.Load())
			}
			// A subsequent explicit call recovers the same document and endpoint.
			realSessionRead(t, s, ctx, `document.getElementById('marker').textContent`, `"original document"`)
			if b.targetID != originalTarget || b.page.TargetID != originalTarget || b.endpoint != endpoint {
				t.Fatalf("recovery changed original target/endpoint: %s %s", b.targetID, b.endpoint)
			}
			if count.Load() != 1 || calls != 1 {
				t.Fatalf("recovery replayed prior work: calls=%d effects=%d", calls, count.Load())
			}
		})
	}
}

func TestRealSessionClosedTargetDoesNotSelectAnotherPage(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	s, b := realSession(t, ctx)
	url, count := realSessionCounter(t)
	realSessionNavigate(t, s, ctx, url)
	originalTarget := b.targetID
	other, err := (proto.TargetCreateTarget{URL: url + "/?other"}).Call(b.browser.Context(ctx))
	if err != nil {
		t.Fatalf("create another test-owned target: %v", err)
	}
	if _, err := (proto.TargetCloseTarget{TargetID: originalTarget}).Call(b.browser.Context(ctx)); err != nil {
		t.Fatalf("close original test-owned target: %v", err)
	}
	for range 2 {
		called := false
		_, err := s.Do(ctx, func(backend Backend) (string, error) {
			called = true
			return backend.Eval(ctx, `fetch('/effect', {method:'POST'}).then(r => r.text())`)
		})
		if !errors.Is(err, ErrTargetClosed) || called || b.targetID != originalTarget {
			t.Fatalf("closed target switched or executed: err=%v called=%v target=%s", err, called, b.targetID)
		}
	}
	list, err := (proto.TargetGetTargets{}).Call(b.browser.Context(ctx))
	if err != nil {
		t.Fatal(err)
	}
	foundOther := false
	for _, info := range list.TargetInfos {
		foundOther = foundOther || info.TargetID == other.TargetID
	}
	if !foundOther || count.Load() != 0 {
		t.Fatalf("other target unavailable or script ran there: other=%v effects=%d", foundOther, count.Load())
	}
}

func TestRealSessionBrowserRestartKeepsOriginalIdentity(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	s, b := realSession(t, ctx)
	url, count := realSessionCounter(t)
	realSessionNavigate(t, s, ctx, url)
	originalEndpoint, originalTarget := b.endpoint, b.targetID
	if b.launcher == nil {
		t.Fatal("original browser is not owned by this test")
	}
	// The original realSession cleanup cannot send Browser.close again after
	// this intentional shutdown. Retire its dead transport only at cleanup.
	t.Cleanup(func() {
		detach(b.browser)
		b.browserCancel()
		b.browser = nil
		b.launcher.Kill()
		b.launcher.Cleanup()
	})
	if err := (proto.BrowserClose{}).Call(b.browser.Context(ctx)); err != nil {
		t.Fatalf("close original test-owned browser: %v", err)
	}
	b.launcher.Kill()

	// A fresh profile beneath the temporary home avoids shutdown/profile-lock
	// races; neither launch discovers or controls any pre-existing browser.
	restarted, err := openRod(ctx, ModeHeadless, "real-session-restarted")
	if err != nil {
		t.Fatalf("restart isolated headless browser: %v", err)
	}
	t.Cleanup(func() {
		if err := restarted.Close(); err != nil {
			t.Errorf("close restarted test-owned browser: %v", err)
		}
	})
	if restarted.obtained != ObtainedLaunched || restarted.endpoint == originalEndpoint || restarted.targetID == originalTarget {
		t.Fatalf("restart did not create a new browser identity: obtained=%v endpoint=%s target=%s", restarted.obtained, restarted.endpoint, restarted.targetID)
	}
	fresh := &Session{name: "real-session-restarted", mode: ModeHeadless, sem: make(chan struct{}, 1), backend: restarted}
	realSessionNavigate(t, fresh, ctx, url)
	realSessionRead(t, fresh, ctx, `window.recoveryMarker = 'restarted page untouched'`, `"restarted page untouched"`)
	calls := 0
	for range 2 {
		out, err := s.Do(ctx, func(backend Backend) (string, error) {
			calls++
			return backend.Eval(ctx, `window.recoveryMarker = 'wrongly selected'; fetch('/effect', {method:'POST'}).then(r => r.text())`)
		})
		if err == nil || ctx.Err() != nil || !strings.Contains(err.Error(), "reconnect original browser") {
			t.Fatalf("expected explicit original-browser recovery error: out=%q err=%v context=%v", out, err, ctx.Err())
		}
		t.Logf("actual restart recovery error: %T: %v", err, err)
		if s.backend != b || b.endpoint != originalEndpoint || b.targetID != originalTarget || calls != 0 || count.Load() != 0 {
			t.Fatalf("restart changed identity or executed callback: endpoint=%s target=%s calls=%d effects=%d", b.endpoint, b.targetID, calls, count.Load())
		}
		realSessionRead(t, fresh, ctx, `window.recoveryMarker`, `"restarted page untouched"`)
	}
}

func TestRealSessionFirstCallCancellationDoesNotPoisonNextCall(t *testing.T) {
	for _, when := range []string{"after-return", "during-call"} {
		t.Run(when, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
			defer cancel()
			firstCtx, cancelFirst := context.WithCancel(ctx)
			defer cancelFirst()
			s, b := realSession(t, firstCtx)
			url, count := realSessionCounter(t)
			originalTarget := b.targetID
			out, err := s.Do(firstCtx, func(backend Backend) (string, error) {
				if err := backend.Navigate(firstCtx, url); err != nil {
					return "", err
				}
				value, err := backend.Eval(firstCtx, `fetch('/effect', {method:'POST'}).then(r => r.text())`)
				if err != nil {
					return "", err
				}
				if when == "during-call" {
					cancelFirst()
					_, err = (proto.PageGetFrameTree{}).Call(b.page.Context(firstCtx))
				}
				return value, err
			})
			if out != `"1"` {
				t.Fatalf("first call output lost: %q, err=%v", out, err)
			}
			if when == "during-call" {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("expected cancellation, got %v", err)
				}
			} else if err != nil {
				t.Fatalf("first call: %v", err)
			}
			cancelFirst()
			realSessionRead(t, s, ctx, `document.getElementById('marker').textContent`, `"original document"`)
			if b.targetID != originalTarget || b.page.TargetID != originalTarget || count.Load() != 1 {
				t.Fatalf("cancellation lost target or replayed effect: target=%s effects=%d", b.targetID, count.Load())
			}
		})
	}
}
