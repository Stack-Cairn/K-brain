package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto"
	"github.com/chromedp/cdproto/target"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

func TestChromedpRecoveryInvalidation(t *testing.T) {
	b := &chromedpBackend{targetID: target.ID("original")}
	b.Invalidate(context.Canceled)
	if b.invalid != nil {
		t.Fatal("caller cancellation invalidated connection")
	}
	b.Invalidate(errors.New("JavaScript error"))
	if b.invalid != nil {
		t.Fatal("ordinary action error invalidated connection")
	}
	b.Invalidate(&cdproto.Error{Code: -32001, Message: "Session with given id not found"})
	if !isSessionErr(b.invalid) {
		t.Fatalf("session failure not retained: %v", b.invalid)
	}
	b.Invalidate(io.EOF)
	if !isConnErr(b.invalid) {
		t.Fatalf("connection failure not retained: %v", b.invalid)
	}
	b.Invalidate(ErrTargetClosed)
	b.Invalidate(io.EOF)
	if !errors.Is(b.Prepare(t.Context()), ErrTargetClosed) {
		t.Fatal("closed target must remain sticky")
	}
	if b.targetID != "original" {
		t.Fatal("invalidation changed target identity")
	}
}

func TestChromedpRecoveryCanceledPrepare(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	b := &chromedpBackend{targetID: "original", invalid: io.EOF}
	if err := b.Prepare(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Prepare = %v", err)
	}
	if b.targetID != "original" || !errors.Is(b.invalid, io.EOF) {
		t.Fatal("canceled preparation changed recovery state")
	}
}

// This fixture exercises CDP lifecycle only; it does not emulate page behavior.
type chromedpRecoveryCDP struct {
	mu         sync.Mutex
	conns      []net.Conn
	methods    []string
	attached   []string
	sessions   int
	failFrame  bool
	failAttach string
	missing    bool
	namedURL   string
}

func (f *chromedpRecoveryCDP) serve(w http.ResponseWriter, r *http.Request) {
	conn, _, _, err := ws.UpgradeHTTP(r, w)
	if err != nil {
		return
	}
	defer conn.Close()
	f.mu.Lock()
	f.conns = append(f.conns, conn)
	f.mu.Unlock()
	for {
		data, _, err := wsutil.ReadClientData(conn)
		if err != nil {
			return
		}
		var req struct {
			ID      int64  `json:"id"`
			Method  string `json:"method"`
			Session string `json:"sessionId"`
			Params  struct {
				TargetID string `json:"targetId"`
				URL      string `json:"url"`
			} `json:"params"`
		}
		if json.Unmarshal(data, &req) != nil {
			return
		}
		result := any(map[string]any{})
		var protocolErr any
		f.mu.Lock()
		f.methods = append(f.methods, req.Method)
		switch req.Method {
		case "Target.getTargets":
			infos := []any{
				map[string]any{"targetId": "internal", "type": "page", "url": "chrome://settings"},
				map[string]any{"targetId": "original", "type": "page", "url": "https://example.test/"},
			}
			if f.namedURL != "" {
				infos = append([]any{map[string]any{"targetId": "named", "type": "page", "url": f.namedURL}}, infos...)
			}
			result = map[string]any{"targetInfos": infos}
		case "Target.createTarget":
			f.namedURL = req.Params.URL
			result = map[string]any{"targetId": "named"}
		case "Page.navigate":
			f.namedURL = req.Params.URL
			result = map[string]any{"frameId": "frame"}
		case "Target.getTargetInfo":
			if f.missing {
				protocolErr = map[string]any{"code": -32602, "message": "No target with given id found"}
			} else {
				result = map[string]any{"targetInfo": map[string]any{"targetId": req.Params.TargetID, "type": "page", "url": "https://example.test/"}}
			}
		case "Target.attachToTarget":
			if req.Params.TargetID == f.failAttach {
				protocolErr = map[string]any{"code": -32602, "message": "No target with given id found"}
				break
			}
			f.sessions++
			f.attached = append(f.attached, req.Params.TargetID)
			result = map[string]any{"sessionId": fmt.Sprintf("session-%d", f.sessions)}
		case "DOM.getDocument":
			result = map[string]any{"root": map[string]any{"nodeId": 1, "backendNodeId": 1, "nodeType": 9, "nodeName": "#document", "localName": "", "nodeValue": ""}}
		case "Runtime.evaluate":
			result = map[string]any{"result": map[string]any{"type": "boolean", "value": false}}
		case "Page.getFrameTree":
			if f.failFrame {
				f.failFrame = false
				protocolErr = map[string]any{"code": -32001, "message": "Session with given id not found"}
			} else {
				result = map[string]any{"frameTree": map[string]any{"frame": map[string]any{"id": "frame", "loaderId": "loader", "url": "https://example.test/", "securityOrigin": "https://example.test", "mimeType": "text/html"}}}
			}
		}
		f.mu.Unlock()
		response := map[string]any{"id": req.ID, "result": result}
		if req.Session != "" {
			response["sessionId"] = req.Session
		}
		if protocolErr != nil {
			delete(response, "result")
			response["error"] = protocolErr
		}
		out, _ := json.Marshal(response)
		if wsutil.WriteServerMessage(conn, ws.OpText, out) != nil {
			return
		}
	}
}

func newChromedpRecoveryCDP(t *testing.T) (*chromedpRecoveryCDP, string) {
	t.Helper()
	f := &chromedpRecoveryCDP{}
	server := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(func() {
		f.mu.Lock()
		for _, c := range f.conns {
			_ = c.Close()
		}
		f.mu.Unlock()
		server.Close()
	})
	return f, "ws" + strings.TrimPrefix(server.URL, "http") + "/devtools/browser/test"
}

func TestChromedpRecoveryFailedSwitchRetainsCurrentTarget(t *testing.T) {
	f, endpoint := newChromedpRecoveryCDP(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	b := &chromedpBackend{mode: ModeLive, endpoint: endpoint, targetID: "original"}
	defer b.Close()
	if err := b.connect(ctx); err != nil {
		t.Fatal(err)
	}
	if err := liveTargets.claim(endpoint, "original", b); err != nil {
		t.Fatal(err)
	}
	if err := b.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	originalContext := b.targetCtx
	f.mu.Lock()
	f.failAttach = "disappearing"
	f.mu.Unlock()
	s := &Session{mode: ModeLive, backend: b, sem: make(chan struct{}, 1)}
	_, err := s.Do(ctx, func(backend Backend) (string, error) { return "", backend.UseTab(ctx, "disappearing") })
	if !errors.Is(err, ErrTargetClosed) {
		t.Fatalf("missing attempted-target error: %v", err)
	}
	if b.targetID != "original" || b.targetCtx != originalContext || originalContext.Err() != nil || b.invalid != nil {
		t.Fatalf("failed switch invalidated current target: target=%s err=%v invalid=%v", b.targetID, originalContext.Err(), b.invalid)
	}
	called := false
	if _, err := s.Do(ctx, func(Backend) (string, error) { called = true; return "", nil }); err != nil || !called {
		t.Fatalf("current target unusable after failed switch: called=%v err=%v", called, err)
	}
	other := &Browser{mode: ModeLive}
	if err := liveTargets.claim(endpoint, "original", other); !errors.Is(err, ErrLiveTargetInUse) {
		t.Fatalf("current target ownership released: %v", err)
	}
	if err := liveTargets.claim(endpoint, "disappearing", other); err != nil {
		t.Fatalf("failed target ownership retained: %v", err)
	}
	liveTargets.releaseAll(other)
}

func TestChromedpRecoveryLifecycle(t *testing.T) {
	f, endpoint := newChromedpRecoveryCDP(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	b := &chromedpBackend{mode: ModeLive, endpoint: endpoint, targetID: "original"}
	defer b.Close()
	if err := b.connect(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.failFrame = true
	f.mu.Unlock()
	// No Invalidate call: Prepare itself must detect the dead session.
	if err := b.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.UseTab(ctx, "chosen"); err != nil {
		t.Fatal(err)
	}
	if b.targetID != "chosen" {
		t.Fatalf("UseTab saved %q", b.targetID)
	}
	f.mu.Lock()
	lastConn := f.conns[len(f.conns)-1]
	f.mu.Unlock()
	_ = lastConn.Close()
	select {
	case <-b.browser.LostConnection:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := b.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	if b.endpoint != endpoint || b.targetID != "chosen" {
		t.Fatal("recovery changed identity")
	}
	cancel()
	next, nextOff := context.WithTimeout(t.Context(), 5*time.Second)
	defer nextOff()
	if err := b.Prepare(next); err != nil {
		t.Fatalf("connection did not outlive caller: %v", err)
	}
	f.mu.Lock()
	f.missing = true
	f.mu.Unlock()
	b.Invalidate(&cdproto.Error{Code: -32001, Message: "Session with given id not found"})
	if err := b.Prepare(next); !isTargetClosedErr(err) {
		t.Fatalf("missing target = %v", err)
	}
	if err := b.Prepare(next); !errors.Is(err, ErrTargetClosed) {
		t.Fatalf("closed target not sticky: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	want := []string{"original", "original", "chosen", "chosen"}
	if fmt.Sprint(f.attached) != fmt.Sprint(want) {
		t.Fatalf("attachments = %v, want %v", f.attached, want)
	}
	for _, method := range f.methods {
		if method == "Target.closeTarget" || method == "Browser.close" || method == "Target.createTarget" {
			t.Fatalf("unexpected lifecycle command %s", method)
		}
	}
}

func TestChromedpRecoveryLiveTargetSelection(t *testing.T) {
	for _, name := range []string{"default", "research"} {
		t.Run(name, func(t *testing.T) {
			f, endpoint := newChromedpRecoveryCDP(t)
			ctx, off := context.WithTimeout(t.Context(), 5*time.Second)
			defer off()
			b := &chromedpBackend{mode: ModeLive, endpoint: endpoint}
			defer b.Close()
			if err := b.connect(ctx); err != nil {
				t.Fatal(err)
			}
			if err := b.selectLiveTarget(ctx, name); err != nil {
				t.Fatal(err)
			}
			want := target.ID("original")
			if name != "default" {
				want = "named"
			}
			if b.targetID != want {
				t.Fatalf("selected %q, want %q", b.targetID, want)
			}
			if err := b.Prepare(ctx); err != nil {
				t.Fatal(err)
			}
			b.Invalidate(&cdproto.Error{Code: -32001, Message: "Session with given id not found"})
			if err := b.Prepare(ctx); err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			creates := 0
			for _, method := range f.methods {
				if method == "Target.createTarget" {
					creates++
				}
			}
			wantCreates := 0
			if name != "default" {
				wantCreates = 1
			}
			if creates != wantCreates {
				t.Fatalf("created %d targets, want %d", creates, wantCreates)
			}
		})
	}
}
