package browser

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/browser/extrelay"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

type fakeExtClient struct {
	nc net.Conn
	br *bufio.Reader
}

func dialRelayExt(t *testing.T, r *extrelay.Relay) *fakeExtClient {
	t.Helper()
	nc, br, _, err := ws.Dial(context.Background(),
		fmt.Sprintf("ws://%s/ext?token=%s", r.Addr(), r.Token()))
	if err != nil {
		t.Fatalf("extension dial: %v", err)
	}
	if br == nil {
		br = bufio.NewReader(nc)
	}
	return &fakeExtClient{nc: nc, br: br}
}

func (f *fakeExtClient) close() { _ = f.nc.Close() }

func (f *fakeExtClient) Read(p []byte) (int, error)  { return f.br.Read(p) }
func (f *fakeExtClient) Write(p []byte) (int, error) { return f.nc.Write(p) }

func (f *fakeExtClient) send(t *testing.T, s string) {
	t.Helper()
	if err := wsutil.WriteClientText(f.nc, []byte(s)); err != nil {
		t.Fatalf("ext write: %v", err)
	}
}

func (f *fakeExtClient) answerLoop(t *testing.T) {
	t.Helper()
	for {
		msg, err := wsutil.ReadServerText(f)
		if err != nil {
			return
		}
		var fr struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
			Params struct {
				Expression string `json:"expression"`
			} `json:"params"`
		}
		if json.Unmarshal(msg, &fr) != nil || fr.ID == 0 {
			continue
		}
		_ = wsutil.WriteClientText(f.nc, []byte(fmt.Sprintf(`{"id":%d,"result":%s}`, fr.ID, fakeCDPResult(fr.Method, fr.Params.Expression))))
	}
}

func fakeCDPResult(method, expr string) string {
	switch method {
	case "Runtime.evaluate":
		if expr == "document.readyState" {
			return `{"result":{"type":"string","value":"complete"}}`
		}
		if strings.Contains(expr, "JSON.stringify") {

			return `{"result":{"type":"string","value":"{\"url\":\"https://example.com/\",\"title\":\"Example\",\"w\":1280,\"h\":800,\"sx\":0,\"sy\":0,\"pw\":1280,\"ph\":800}"}}`
		}
		return `{"result":{"type":"string","value":"Example"}}`
	case "Runtime.callFunctionOn":
		return `{"result":{"type":"string","value":"Example"}}`
	case "Page.getFrameTree":
		return `{"frameTree":{"frame":{"id":"tab-7","url":"https://example.com/"}}}`
	case "Page.getLayoutMetrics":
		return `{"layoutViewport":{"clientWidth":1280,"clientHeight":800},"cssContentSize":{"width":1280,"height":800},"contentSize":{"width":1280,"height":800}}`
	case "Page.captureScreenshot":
		return `{"data":"/9j/4AAQSkZJRg=="}`
	case "Accessibility.getFullAXTree":
		return `{"nodes":[{"nodeId":"1","role":{"value":"RootWebArea"},"name":{"value":"Example"}}]}`
	case "DOM.getBoxModel":
		return `{"model":{"content":[0,0,10,0,10,10,0,10],"width":10,"height":10}}`
	default:

		return `{}`
	}
}
