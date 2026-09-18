package extrelay

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/gobwas/ws/wsutil"
)

func TestRodThroughRelay(t *testing.T) {
	r, err := NewRelay()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	ext := dialWS(t, fmt.Sprintf("ws://%s/ext?token=%s", r.Addr(), r.Token()))
	defer ext.Close()

	writeCli(t, ext, `{"method":"k-brain.attached","params":{"tabId":7,"title":"Example","url":"https://example.com/"}}`)
	time.Sleep(100 * time.Millisecond)

	go func() {
		for {
			msg, err := wsutil.ReadServerText(ext)
			if err != nil {
				return
			}
			var f struct {
				ID     int64           `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if json.Unmarshal(msg, &f) != nil || f.ID == 0 {
				continue
			}
			answer(ext, f.ID, f.Method)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	b := rod.New().ControlURL(r.CDPURL()).Context(ctx)

	if err := b.Connect(); err != nil {
		t.Fatalf("rod connect through relay: %v", err)
	}
	defer b.Close()

	pages, err := b.Pages()
	if err != nil {
		t.Fatalf("rod Pages through relay: %v", err)
	}
	if len(pages) != 1 {
		t.Fatalf("want exactly one page target, got %d", len(pages))
	}

	val, err := pages[0].Context(ctx).Eval(`() => document.title`)
	if err != nil {
		t.Fatalf("rod Eval through relay: %v", err)
	}
	if val.Value.Str() != "Example" {
		t.Fatalf("eval round-trip: got %q", val.Value.Str())
	}
}

func answer(ext *client, id int64, method string) {
	var result string
	switch method {
	case "Runtime.evaluate":
		result = `{"result":{"type":"string","value":"Example"}}`
	case "Runtime.callFunctionOn":

		result = `{"result":{"type":"string","value":"Example"}}`
	case "Page.getFrameTree":
		result = `{"frameTree":{"frame":{"id":"tab-7","url":"https://example.com/"},"childFrames":[]}}`
	default:

		result = `{}`
	}
	_ = wsutil.WriteClientText(ext.nc, []byte(fmt.Sprintf(`{"id":%d,"result":%s}`, id, result)))
}
