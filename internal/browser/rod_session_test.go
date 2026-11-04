package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/cdp"
	"github.com/go-rod/rod/lib/proto"
)

// Protocol fault injection covers recovery without manipulating a user's tabs.
// Real browser/profile acceptance is tracked separately.
type recoveryCDP struct {
	mu              sync.Mutex
	events          chan *cdp.Event
	targets         []string
	sessions        map[string]bool
	attached        []string
	created         int
	broken          bool
	block           chan struct{}
	attachSeen      chan struct{}
	destroyOnFrame  bool
	missingOnAttach bool
}

func (c *recoveryCDP) Event() <-chan *cdp.Event { return c.events }

func (c *recoveryCDP) Call(ctx context.Context, sessionID, method string, params interface{}) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.broken {
		return nil, io.EOF
	}
	switch method {
	case "Target.getTargets":
		var infos []map[string]any
		for _, id := range c.targets {
			infos = append(infos, map[string]any{"targetId": id, "type": "page", "url": "https://example.com/" + id, "title": id})
		}
		return json.Marshal(map[string]any{"targetInfos": infos})
	case "Target.createTarget":
		c.created++
		id := fmt.Sprintf("created-%d", c.created)
		c.targets = append(c.targets, id)
		return json.Marshal(map[string]any{"targetId": id})
	case "Target.attachToTarget":
		if c.block != nil {
			if c.attachSeen != nil {
				close(c.attachSeen)
				c.attachSeen = nil
			}
			block := c.block
			c.mu.Unlock()
			select {
			case <-block:
			case <-ctx.Done():
			}
			c.mu.Lock()
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		var p struct {
			TargetID string `json:"targetId"`
		}
		data, _ := json.Marshal(params)
		_ = json.Unmarshal(data, &p)
		found := false
		for _, id := range c.targets {
			found = found || id == p.TargetID
		}
		if !found || c.missingOnAttach {
			return nil, &cdp.Error{Code: -32602, Message: "No target with given id found"}
		}
		c.attached = append(c.attached, p.TargetID)
		sid := fmt.Sprintf("session-%d", len(c.attached))
		c.sessions[sid] = true
		return json.Marshal(map[string]any{"sessionId": sid})
	case "Target.detachFromTarget":
		var p struct {
			SessionID string `json:"sessionId"`
		}
		data, _ := json.Marshal(params)
		_ = json.Unmarshal(data, &p)
		delete(c.sessions, p.SessionID)
	case "Page.getFrameTree":
		if c.destroyOnFrame {
			c.destroyOnFrame = false
			c.mu.Unlock()
			c.events <- &cdp.Event{Method: "Target.targetDestroyed", Params: json.RawMessage(`{"targetId":"original"}`)}
			<-ctx.Done()
			c.mu.Lock()
			return nil, ctx.Err()
		}
		if !c.sessions[sessionID] {
			return nil, &cdp.Error{Code: -32001, Message: "Session with given id not found"}
		}
		return []byte(`{"frameTree":{"frame":{"id":"frame","url":"https://example.com/"}}}`), nil
	}
	return []byte(`{}`), nil
}

func newRecoveryCDP(t *testing.T) (*rod.Browser, *recoveryCDP) {
	t.Helper()
	c := &recoveryCDP{events: make(chan *cdp.Event), targets: []string{"original", "other"}, sessions: make(map[string]bool)}
	ctx, cancel := context.WithCancel(t.Context())
	rb := rod.New().Context(ctx).NoDefaultDevice().Client(c)
	if err := rb.Connect(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); close(c.events) })
	return rb, c
}

func newRecoveryBrowser(t *testing.T, callCtx context.Context) (*Browser, *recoveryCDP) {
	t.Helper()
	rb, c := newRecoveryCDP(t)
	b := &Browser{mode: ModeLive, obtained: ObtainedLive, endpoint: "ws://original/devtools/browser/identity"}
	b.keepBrowser(callCtx, rb)
	t.Cleanup(func() { _ = b.Close() })
	if err := b.attachPage(callCtx, "default"); err != nil {
		t.Fatal(err)
	}
	return b, c
}

func TestRodReattachesSameTargetAfterSessionLoss(t *testing.T) {
	b, c := newRecoveryBrowser(t, t.Context())
	old := b.page.SessionID
	c.mu.Lock()
	delete(c.sessions, string(old))
	c.mu.Unlock()
	if err := b.Prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	if b.targetID != "original" || b.page.SessionID == old || c.created != 0 || len(c.attached) != 2 || c.attached[1] != "original" {
		t.Fatalf("wrong reattach: target=%s session=%s attached=%v created=%d", b.targetID, b.page.SessionID, c.attached, c.created)
	}
}

func TestRodClosedTargetDoesNotSelectAnotherPage(t *testing.T) {
	b, c := newRecoveryBrowser(t, t.Context())
	c.mu.Lock()
	c.targets = []string{"other"}
	c.mu.Unlock()
	for range 2 {
		if err := b.Prepare(t.Context()); !errors.Is(err, ErrTargetClosed) {
			t.Fatalf("want closed original target, got %v", err)
		}
	}
	if b.targetID != "original" || c.created != 0 || len(c.attached) != 1 {
		t.Fatalf("closed target switched pages: %s %v", b.targetID, c.attached)
	}
}

func TestRodTargetClosureDoesNotWrapInternalCancellation(t *testing.T) {
	b, c := newRecoveryBrowser(t, t.Context())
	c.mu.Lock()
	c.targets = []string{"other"}
	c.mu.Unlock()
	err := b.targetError(t.Context(), b.targetID, context.Canceled)
	if !errors.Is(err, ErrTargetClosed) || errors.Is(err, context.Canceled) {
		t.Fatalf("internal page cancellation must report target closure, got %v", err)
	}
}

func TestRodSessionOutlivesFirstCall(t *testing.T) {
	callCtx, cancel := context.WithCancel(t.Context())
	b, c := newRecoveryBrowser(t, callCtx)
	cancel()
	if err := b.Prepare(t.Context()); err != nil {
		t.Fatalf("first call cancellation poisoned established session: %v", err)
	}
	if len(c.attached) != 1 {
		t.Fatalf("unnecessary reattach after normal call completion: %v", c.attached)
	}
}

func TestRodReconnectKeepsEndpointAndTarget(t *testing.T) {
	b, old := newRecoveryBrowser(t, t.Context())
	old.broken = true
	rb, next := newRecoveryCDP(t)
	orig := connectLive
	t.Cleanup(func() { connectLive = orig })
	connects := 0
	connectLive = func(_ context.Context, ws string) (*rod.Browser, error) {
		connects++
		if ws != b.endpoint {
			t.Fatalf("changed original endpoint: %s", ws)
		}
		return rb, nil
	}
	if err := b.Prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	if connects != 1 || b.targetID != "original" || next.created != 0 || len(next.attached) != 1 || next.attached[0] != "original" {
		t.Fatalf("reconnected to wrong page: connects=%d target=%s attached=%v", connects, b.targetID, next.attached)
	}
}

func TestRodNamedLiveSessionGetsOwnTarget(t *testing.T) {
	rb, c := newRecoveryCDP(t)
	b := &Browser{mode: ModeLive}
	b.keepBrowser(t.Context(), rb)
	t.Cleanup(func() { _ = b.Close() })
	if err := b.attachPage(t.Context(), "independent"); err != nil {
		t.Fatal(err)
	}
	if b.targetID != "created-1" || len(c.attached) != 1 || c.attached[0] != "created-1" {
		t.Fatalf("named session reused a user's existing target: %s %v", b.targetID, c.attached)
	}
}

func TestRodRecoveryAttachRespectsCancellation(t *testing.T) {
	b, c := newRecoveryBrowser(t, t.Context())
	b.Invalidate(&cdp.Error{Code: -32001, Message: "Session with given id not found"})
	c.block, c.attachSeen = make(chan struct{}), make(chan struct{})
	seen := c.attachSeen
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- b.Prepare(ctx) }()
	<-seen
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled recovery: %v", err)
	}
	if b.targetID != proto.TargetTargetID("original") {
		t.Fatal("cancelled recovery lost original target")
	}
}

func TestRodDestroyedTargetInterruptsHealthCheck(t *testing.T) {
	b, c := newRecoveryBrowser(t, t.Context())
	c.mu.Lock()
	c.destroyOnFrame = true
	c.mu.Unlock()
	if err := b.Prepare(t.Context()); !errors.Is(err, ErrTargetClosed) {
		t.Fatalf("destroyed target with stale target list: %v", err)
	}
	if b.targetID != "original" || b.page != nil || len(c.attached) != 1 {
		t.Fatalf("destroyed target was replaced: target=%s attached=%v", b.targetID, c.attached)
	}
}

func TestRodClosedDuringAttachNormalizesError(t *testing.T) {
	b, c := newRecoveryBrowser(t, t.Context())
	b.Invalidate(&cdp.Error{Code: -32001, Message: "Session with given id not found"})
	c.missingOnAttach = true
	if err := b.Prepare(t.Context()); !errors.Is(err, ErrTargetClosed) {
		t.Fatalf("closed attach should retain sentinel: %v", err)
	}
}

func TestRodFailedSwitchThroughSessionRetainsCurrentTarget(t *testing.T) {
	b, c := newRecoveryBrowser(t, t.Context())
	originalPage, originalID := b.page, b.targetID
	s := &Session{name: "recovery-switch", mode: ModeLive, sem: make(chan struct{}, 1), backend: b}
	_, err := s.Do(t.Context(), func(backend Backend) (string, error) {
		return "", backend.UseTab(t.Context(), "missing")
	})
	if !errors.Is(err, ErrTargetClosed) {
		t.Fatalf("missing requested target: %v", err)
	}
	if b.page != originalPage || b.targetID != originalID || b.page.GetContext().Err() != nil {
		t.Fatalf("failed switch invalidated retained page: target=%s", b.targetID)
	}
	contender := &Browser{mode: ModeLive}
	if err := liveTargets.claim(b.endpoint, string(originalID), contender); err == nil {
		liveTargets.release(b.endpoint, string(originalID), contender)
		t.Fatal("failed switch released original target lease")
	}
	if err := liveTargets.claim(b.endpoint, "missing", contender); err != nil {
		t.Fatalf("failed switch retained candidate lease: %v", err)
	}
	liveTargets.release(b.endpoint, "missing", contender)
	if err := b.Prepare(t.Context()); err != nil {
		t.Fatalf("retained target cannot prepare: %v", err)
	}
	if len(c.attached) != 1 {
		t.Fatalf("retained target unnecessarily reattached: %v", c.attached)
	}
}

func TestLiveFailureDoesNotStartDedicated(t *testing.T) {
	t.Setenv("K_BRAIN_CDP_WS", "ws://unavailable/devtools/browser/x")
	t.Setenv("ROD_BROWSER_BIN", "this-browser-must-not-be-launched")
	orig := connectLive
	t.Cleanup(func() { connectLive = orig })
	connectLive = func(context.Context, string) (*rod.Browser, error) { return nil, errLiveRefused }
	_, err := openRod(t.Context(), ModeLive, "default")
	if !errors.Is(err, ErrNoLiveBrowser) || err == nil {
		t.Fatalf("missing live endpoint should surface: %v", err)
	}
	if isConnErr(err) {
		t.Fatal("unavailable live browser is not a transient script connection failure")
	}
}
