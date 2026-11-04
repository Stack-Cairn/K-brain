package browser

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/Stack-Cairn/K-brain/internal/browser/extrelay"
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
	b := &Browser{mode: ModeExtension, obtained: ObtainedLive, endpoint: r.CDPURL()}
	rb, err := connectLive(ctx, b.endpoint)
	if err != nil {
		return nil, fmt.Errorf("connect to extension relay: %w", err)
	}
	b.keepBrowser(ctx, rb)
	if err := b.attachPage(ctx, "default"); err != nil {
		if b.targetID != "" {
			b.Invalidate(err)
			return b, fmt.Errorf("attach to pinned tab: %w", err)
		}
		detach(b.browser)
		b.browserCancel()
		return nil, fmt.Errorf("attach to pinned tab: %w", err)
	}
	return b, nil
}
