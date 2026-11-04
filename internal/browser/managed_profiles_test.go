package browser

import (
	"context"
	"strings"
	"testing"
	"time"

	cdpbrowser "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/go-rod/rod/lib/proto"
)

func TestManagedModeIsolation(t *testing.T) {
	t.Setenv("ROD_BROWSER_BIN", chromiumPath(t))
	oldDriver := Driver
	t.Cleanup(func() { Driver = oldDriver })
	for _, driver := range Drivers {
		t.Run(driver, func(t *testing.T) {
			Driver = driver
			for _, name := range []string{"default", "shared"} {
				t.Run(name, func(t *testing.T) {
					dataDir := t.TempDir()
					t.Setenv("LIVEAGENT_HOME", dataDir)
					t.Setenv("K_BRAIN_HOME", dataDir)
					ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
					defer cancel()
					open := func(mode Mode) Backend {
						t.Helper()
						b, err := OpenNamed(ctx, mode, name)
						if b != nil {
							t.Cleanup(func() {
								_ = b.Close()
								if rb, ok := b.(*Browser); ok && rb.launcher != nil {
									rb.launcher.Kill()
									rb.launcher.Cleanup()
								}
							})
						}
						if err != nil {
							t.Fatalf("open %s: %v", mode, err)
						}
						return b
					}
					dedicated := open(ModeDedicated)
					if _, err := dedicated.Eval(ctx, `globalThis.__profileIsolation = "dedicated"`); err != nil {
						t.Fatal(err)
					}
					headless := open(ModeHeadless)
					if _, err := headless.Eval(ctx, `globalThis.__profileIsolation = "headless"`); err != nil {
						t.Fatal(err)
					}
					marker, err := dedicated.Eval(ctx, `globalThis.__profileIsolation`)
					if err != nil || marker != `"dedicated"` {
						t.Fatalf("headless overwrote dedicated page: marker=%s err=%v", marker, err)
					}
					if dedicated.Mode() != ModeDedicated || headless.Mode() != ModeHeadless {
						t.Fatalf("modes changed: dedicated=%s headless=%s", dedicated.Mode(), headless.Mode())
					}
					var userAgent string
					switch b := headless.(type) {
					case *Browser:
						version, versionErr := (proto.BrowserGetVersion{}).Call(b.browser.Context(ctx))
						err = versionErr
						if version != nil {
							userAgent = version.UserAgent
						}
					case *chromedpBackend:
						_, _, _, userAgent, _, err = cdpbrowser.GetVersion().Do(cdp.WithExecutor(ctx, b.browser))
					}
					if err != nil || !strings.Contains(userAgent, "HeadlessChrome") {
						t.Fatalf("headless session used a visible browser: userAgent=%s err=%v", userAgent, err)
					}
				})
			}
		})
	}
}
