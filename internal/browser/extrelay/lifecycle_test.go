package extrelay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/gobwas/ws/wsutil"
)

func TestRelayCloseTerminatesSockets(t *testing.T) {
	r, err := NewRelay()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	ext := dialWS(t, fmt.Sprintf("ws://%s/ext?token=%s", r.Addr(), r.Token()))
	t.Cleanup(func() { _ = ext.Close() })
	cdp := dialWS(t, r.CDPURL())
	t.Cleanup(func() { _ = cdp.Close() })
	if !r.Attached() {
		t.Fatal("handshake finished before extension registration")
	}
	done := make(chan error, 8)
	for range 8 {
		go func() { done <- r.Close() }()
	}
	for range 8 {
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Close: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Close did not join connection handlers")
		}
	}
	if r.Attached() {
		t.Fatal("closed relay retained an extension")
	}
	for _, c := range []*client{ext, cdp} {
		_ = c.nc.SetReadDeadline(time.Now().Add(time.Second))
		_, err := wsutil.ReadServerText(c)
		if err == nil {
			t.Fatal("closed relay retained an upgraded socket")
		}
		var timeout net.Error
		if errors.As(err, &timeout) && timeout.Timeout() {
			t.Fatalf("socket remained open after relay Close: %v", err)
		}
	}
	if err := r.WaitAttached(context.Background()); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("WaitAttached after Close = %v", err)
	}
}

type gatedEOF struct {
	started chan struct{}
	release chan struct{}
}

func (r *gatedEOF) Read([]byte) (int, error) {
	close(r.started)
	<-r.release
	return 0, io.EOF
}

func TestOldCDPCleanupPreservesReplacement(t *testing.T) {
	nc, peer := net.Pipe()
	defer nc.Close()
	defer peer.Close()
	reader := &gatedEOF{started: make(chan struct{}), release: make(chan struct{})}
	old := &conn{nc: nc, r: reader}
	replacement := &conn{}
	r := &Relay{cdpConn: old}
	done := make(chan struct{})
	go func() { r.serveCDP(old); close(done) }()
	<-reader.started
	r.mu.Lock()
	r.cdpConn = replacement
	r.mu.Unlock()
	close(reader.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("old CDP handler never exited")
	}
	r.mu.Lock()
	current := r.cdpConn
	r.mu.Unlock()
	if current != replacement {
		t.Fatal("old CDP cleanup erased the replacement")
	}
}

func TestReplacedCDPStillReceivesExtensionReplies(t *testing.T) {
	r, err := NewRelay()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ext := dialWS(t, fmt.Sprintf("ws://%s/ext?token=%s", r.Addr(), r.Token()))
	defer ext.Close()
	cdp := dialWS(t, r.CDPURL())
	defer func() { _ = cdp.Close() }()
	for i := 1; i <= 20; i++ {
		next := dialWS(t, r.CDPURL())
		_ = cdp.Close()
		cdp = next
		_ = ext.nc.SetDeadline(time.Now().Add(2 * time.Second))
		_ = cdp.nc.SetDeadline(time.Now().Add(2 * time.Second))
		request := fmt.Sprintf(`{"id":%d,"method":"Runtime.evaluate"}`, i)
		writeCli(t, cdp, request)
		if got := readSrv(t, ext); got != request {
			t.Fatalf("request was not forwarded: %s", got)
		}
		reply := fmt.Sprintf(`{"id":%d,"result":{}}`, i)
		writeCli(t, ext, reply)
		if got := readSrv(t, cdp); got != reply {
			t.Fatalf("reply lost after replacement: %s", got)
		}
	}
}

func TestStaleExtensionCannotOverwriteTab(t *testing.T) {
	old, current := &conn{}, &conn{}
	r := &Relay{ext: current, tabInfo: tabInfo{ID: "current"}}
	r.handleControl(old, []byte(`{"method":"k-brain.attached","params":{"tabId":99}}`))
	if r.tabInfo.ID != "current" {
		t.Fatal("stale extension overwrote active tab")
	}
	r.handleControl(current, []byte(`{"method":"k-brain.attached","params":{"tabId":42}}`))
	if r.tabInfo.ID != "tab-42" {
		t.Fatal("active extension could not set its tab")
	}
}

func TestWaitAttachedPreservesCancellation(t *testing.T) {
	r, err := NewRelay()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.WaitAttached(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation cause lost: %v", err)
	}
}
