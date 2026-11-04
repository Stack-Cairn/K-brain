package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/browser/extrelay"
	"github.com/gobwas/ws/wsutil"
)

func TestExtensionKeepsTargetAfterInitialAttachFailure(t *testing.T) {
	rel, err := extrelay.NewRelay()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rel.Close() })
	sharedRelayMu.Lock()
	original := sharedRelay
	sharedRelay = rel
	sharedRelayMu.Unlock()
	t.Cleanup(func() {
		sharedRelayMu.Lock()
		sharedRelay = original
		sharedRelayMu.Unlock()
	})
	ext := dialRelayExt(t, rel)
	t.Cleanup(ext.close)
	var writer sync.Mutex
	write := func(message string) error {
		writer.Lock()
		defer writer.Unlock()
		return wsutil.WriteClientText(ext.nc, []byte(message))
	}
	if err := write(`{"method":"k-brain.attached","params":{"tabId":7,"title":"Original","url":"https://example.com/"}}`); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		failed := false
		for {
			message, err := wsutil.ReadServerText(ext)
			if err != nil {
				return
			}
			var frame struct {
				ID     int64  `json:"id"`
				Method string `json:"method"`
			}
			if json.Unmarshal(message, &frame) != nil || frame.ID == 0 {
				continue
			}
			var response string
			if frame.Method == "Page.getFrameTree" && !failed {
				failed = true
				response = fmt.Sprintf(`{"id":%d,"error":{"code":-32001,"message":"Session with given id not found."}}`, frame.ID)
			} else {
				response = fmt.Sprintf(`{"id":%d,"result":%s}`, frame.ID, fakeCDPResult(frame.Method, ""))
			}
			if write(response) != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { ext.close(); <-done })
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	b, err := openExtension(ctx)
	if b == nil || !isSessionErr(err) {
		t.Fatalf("initial attachment lost target: backend=%v err=%v", b, err)
	}
	t.Cleanup(func() { _ = b.Close() })
	if b.targetID != "tab-7" {
		t.Fatalf("original target = %s", b.targetID)
	}
	if err := write(`{"method":"k-brain.attached","params":{"tabId":8,"title":"Other","url":"https://example.org/"}}`); err != nil {
		t.Fatal(err)
	}
	for {
		tabs, err := b.Tabs(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(tabs) == 1 && tabs[0].TargetID == "tab-8" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	s := &Session{mode: ModeExtension, sem: make(chan struct{}, 1), backend: b}
	called := false
	_, err = s.Do(ctx, func(Backend) (string, error) { called = true; return "", nil })
	if called || !errors.Is(err, ErrTargetClosed) || b.targetID != "tab-7" {
		t.Fatalf("repin silently replaced failed target: called=%v target=%s err=%v", called, b.targetID, err)
	}
}
