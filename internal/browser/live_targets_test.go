package browser

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/cdp"
	"github.com/go-rod/rod/lib/proto"
)

func TestLiveTargetClaims(t *testing.T) {
	var registry liveTargetRegistry
	rodOwner := &Browser{mode: ModeLive}
	chromedpOwner := &chromedpBackend{mode: ModeLive}
	mustClaim := func(endpoint, id string, owner Backend) {
		t.Helper()
		if err := registry.claim(endpoint, id, owner); err != nil {
			t.Fatal(err)
		}
	}
	mustClaim("endpoint-a", "tab", rodOwner)
	mustClaim("endpoint-a", "tab", rodOwner) // Recovery keeps the same owner.
	if err := registry.claim("endpoint-a", "tab", chromedpOwner); !errors.Is(err, ErrLiveTargetInUse) {
		t.Fatalf("cross-driver claim = %v", err)
	}
	mustClaim("endpoint-b", "tab", chromedpOwner)
	mustClaim("endpoint-a", "other", chromedpOwner)
	registry.release("endpoint-a", "tab", chromedpOwner)
	if err := registry.claim("endpoint-a", "tab", chromedpOwner); !errors.Is(err, ErrLiveTargetInUse) {
		t.Fatalf("non-owner released a tab: %v", err)
	}
	registry.releaseAll(rodOwner)
	mustClaim("endpoint-a", "tab", chromedpOwner)
	registry.release("endpoint-a", "other", chromedpOwner)
	mustClaim("endpoint-a", "other", rodOwner)
	registry.releaseAll(chromedpOwner)
	mustClaim("endpoint-b", "tab", rodOwner)
	mustClaim("endpoint-a", "other", rodOwner)
}

func TestLiveTargetClaimIsAtomic(t *testing.T) {
	var registry liveTargetRegistry
	const contenders = 32
	var ready sync.WaitGroup
	start := make(chan struct{})
	results := make(chan Backend, contenders)
	for range contenders {
		ready.Add(1)
		go func() {
			owner := &Browser{mode: ModeLive}
			ready.Done()
			<-start
			if registry.claim("endpoint", "tab", owner) == nil {
				results <- owner
			} else {
				results <- nil
			}
		}()
	}
	ready.Wait()
	close(start)
	winners := 0
	for range contenders {
		if <-results != nil {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("atomic claim had %d winners", winners)
	}
}

func newLeasedLiveBackend(t *testing.T, ctx context.Context, driver, endpoint, name string) Backend {
	t.Helper()
	if driver == DriverChromedp {
		b := &chromedpBackend{mode: ModeLive, endpoint: endpoint}
		t.Cleanup(func() { _ = b.Close() })
		if err := b.connect(ctx); err != nil {
			t.Fatal(err)
		}
		if err := b.selectLiveTarget(ctx, name); err != nil {
			t.Fatal(err)
		}
		if err := b.Prepare(ctx); err != nil {
			t.Fatal(err)
		}
		return b
	}
	life, cancel := context.WithCancel(context.WithoutCancel(ctx))
	t.Cleanup(cancel)
	// Rod's default handshake key is "nil"; this strict fixture requires a
	// valid RFC 6455 nonce. Use Rod's transport with an explicit test nonce.
	socket := &cdp.WebSocket{}
	if err := socket.Connect(ctx, endpoint, http.Header{"Sec-WebSocket-Key": {"dGhlIHNhbXBsZSBub25jZQ=="}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = socket.Close() })
	rb := rod.New().Context(life).NoDefaultDevice().Client(cdp.New().Start(socket))
	if err := rb.Connect(); err != nil {
		t.Fatal(err)
	}
	b := &Browser{mode: ModeLive, endpoint: endpoint}
	b.keepBrowser(ctx, rb)
	t.Cleanup(func() { _ = b.Close() })
	if err := b.attachPage(ctx, name); err != nil {
		t.Fatal(err)
	}
	return b
}

func leasedTargetID(b Backend) string {
	switch b := b.(type) {
	case *Browser:
		return string(b.targetID)
	case *chromedpBackend:
		return string(b.targetID)
	default:
		panic("unexpected test backend")
	}
}

func TestLiveTargetNamedThenDefault(t *testing.T) {
	for _, namedDriver := range []string{DriverRod, DriverChromedp} {
		for _, defaultDriver := range []string{DriverRod, DriverChromedp} {
			t.Run(fmt.Sprintf("%s-to-%s", namedDriver, defaultDriver), func(t *testing.T) {
				f, endpoint := newChromedpRecoveryCDP(t)
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				named := newLeasedLiveBackend(t, ctx, namedDriver, endpoint, "research")
				// Navigate the named tab so it is the first ordinary page in getTargets.
				const destination = "https://research.example.test/"
				var err error
				switch b := named.(type) {
				case *Browser:
					_, err = (proto.PageNavigate{URL: destination}).Call(b.page.Context(ctx))
				case *chromedpBackend:
					err = b.run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
						_, _, _, _, err := page.Navigate(destination).Do(ctx)
						return err
					}))
				}
				if err != nil {
					t.Fatal(err)
				}
				f.mu.Lock()
				navigated := f.namedURL
				f.mu.Unlock()
				if navigated != destination {
					t.Fatalf("named tab did not navigate: %q", navigated)
				}
				ordinary := newLeasedLiveBackend(t, ctx, defaultDriver, endpoint, "default")
				if leasedTargetID(named) != "named" || leasedTargetID(ordinary) != "original" {
					t.Fatalf("default took named tab: named=%s default=%s", leasedTargetID(named), leasedTargetID(ordinary))
				}
				f.mu.Lock()
				attachments := len(f.attached)
				f.mu.Unlock()
				if err := ordinary.UseTab(ctx, "named"); !errors.Is(err, ErrLiveTargetInUse) {
					t.Fatalf("explicit occupied UseTab = %v", err)
				}
				f.mu.Lock()
				after := len(f.attached)
				f.mu.Unlock()
				if after != attachments || leasedTargetID(ordinary) != "original" {
					t.Fatal("rejected UseTab changed attachment")
				}
				named.Invalidate(&cdproto.Error{Code: -32001, Message: "Session with given id not found"})
				if err := named.Prepare(ctx); err != nil {
					t.Fatal(err)
				}
				if err := ordinary.UseTab(ctx, "named"); !errors.Is(err, ErrLiveTargetInUse) {
					t.Fatalf("recovery lost ownership: %v", err)
				}
				if err := named.Close(); err != nil {
					t.Fatal(err)
				}
				if err := ordinary.UseTab(ctx, "named"); err != nil {
					t.Fatalf("Close did not release tab: %v", err)
				}
				probe := &Browser{mode: ModeLive}
				defer liveTargets.releaseAll(probe)
				if err := liveTargets.claim(endpoint, "original", probe); err != nil {
					t.Fatalf("UseTab retained old lease: %v", err)
				}
				if err := ordinary.Prepare(ctx); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
