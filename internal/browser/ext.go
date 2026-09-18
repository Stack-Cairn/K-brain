package browser

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/Stack-Cairn/K-brain/internal/browser/extrelay"
	"github.com/go-rod/rod"
)

var (
	sharedRelayMu sync.Mutex
	sharedRelay   *extrelay.Relay
)

func relayForExtension() (*extrelay.Relay, error) {
	sharedRelayMu.Lock()
	defer sharedRelayMu.Unlock()
	if sharedRelay != nil {
		return sharedRelay, nil
	}
	r, err := extrelay.NewRelay()
	if err != nil {
		return nil, err
	}

	if home, herr := os.UserHomeDir(); herr == nil {
		if _, werr := extrelay.WriteRelayState(home, r.Addr(), r.Token()); werr != nil {

			_ = werr
		}
	}
	sharedRelay = r
	return r, nil
}

func openExtension(ctx context.Context) (*Browser, error) {
	r, err := relayForExtension()
	if err != nil {
		return nil, fmt.Errorf("start extension relay: %w", err)
	}
	if err := r.WaitAttached(ctx); err != nil {
		return nil, fmt.Errorf("extension relay waiting for a pinned tab: %w\n(run `kn browser install`, load the extension, then click its icon on the tab to drive)", err)
	}
	b := &Browser{mode: ModeExtension, obtained: ObtainedLive}
	b.browser = rod.New().ControlURL(r.CDPURL())
	if err := b.browser.Connect(); err != nil {
		return nil, fmt.Errorf("connect to extension relay: %w", err)
	}
	b.browser = b.browser.Context(ctx)
	if err := b.attachPage(); err != nil {
		detach(b.browser)
		return nil, fmt.Errorf("attach to pinned tab: %w", err)
	}
	return b, nil
}
