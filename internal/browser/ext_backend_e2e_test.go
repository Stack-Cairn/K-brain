package browser

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/browser/extrelay"
	"github.com/go-rod/rod"
)

func TestBackendThroughRelay(t *testing.T) {
	rel, err := extrelay.NewRelay()
	if err != nil {
		t.Fatal(err)
	}
	defer rel.Close()

	ext := dialRelayExt(t, rel)
	defer ext.close()
	ext.send(t, `{"method":"k-brain.attached","params":{"tabId":7,"title":"Example","url":"https://example.com/"}}`)
	time.Sleep(100 * time.Millisecond)
	go ext.answerLoop(t)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	b := &Browser{mode: ModeExtension, obtained: ObtainedLive}
	b.browser = rod.New().ControlURL(rel.CDPURL())
	if err := b.browser.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	b.browser = b.browser.Context(ctx)
	if err := b.attachPage(); err != nil {
		t.Fatalf("attachPage: %v", err)
	}
	defer b.Close()

	if err := b.Navigate(ctx, "https://example.com/"); err != nil {
		t.Errorf("Navigate: %v", err)
	}
	if err := b.ClickAt(ctx, 10, 10); err != nil {
		t.Errorf("ClickAt: %v", err)
	}
	if err := b.TypeText(ctx, "hello"); err != nil {
		t.Errorf("TypeText: %v", err)
	}
	jpeg, err := b.Screenshot(ctx, 1568)
	if err != nil || len(jpeg) == 0 {
		t.Errorf("Screenshot: %v len=%d", err, len(jpeg))
	}
	ax, err := b.AXTree(ctx)
	if err != nil || !strings.Contains(ax, "Example") {
		t.Errorf("AXTree: %v (%.120s)", err, ax)
	}
	if _, err := b.Eval(ctx, "document.title"); err != nil {
		t.Errorf("Eval: %v", err)
	}
	info, err := b.Info(ctx)
	if err != nil || !strings.Contains(info.URL, "example.com") {
		t.Errorf("Info: %v %+v", err, info)
	}
}
