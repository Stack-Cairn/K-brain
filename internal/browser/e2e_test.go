package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod"
)

var chromiumCandidates = []string{
	"~/.cache/ms-playwright/chromium-1234/chrome-linux64/chrome",
	"~/.cache/ms-playwright/chromium_headless_shell-1234/chrome-headless-shell-linux64/chrome-headless-shell",

	"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
	"/Applications/Chromium.app/Contents/MacOS/Chromium",
	"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
}

func chromeForTestingPath() string {
	home, _ := os.UserHomeDir()
	p := filepath.Join(home, "Library/Caches/ms-playwright/chromium-1234/chrome-mac-arm64/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

func chromiumPath(t *testing.T) string {
	t.Helper()

	libs := "/tmp/chromelibs/usr/lib/x86_64-linux-gnu"
	if _, err := os.Stat(libs); err == nil {
		t.Setenv("LD_LIBRARY_PATH", libs+":"+os.Getenv("LD_LIBRARY_PATH"))
	}
	home, _ := os.UserHomeDir()
	for _, c := range chromiumCandidates {
		p := strings.Replace(c, "~", home, 1)
		if _, err := os.Stat(p); err == nil {
			t.Setenv("ROD_BROWSER_BIN", p)
			return p
		}
	}
	for _, name := range []string{"google-chrome", "chromium", "chromium-browser"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	t.Skip("no chromium-family binary found")
	return ""
}

func testPage(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if after, ok := strings.CutPrefix(r.URL.Path, "/marker/"); ok {
			marker := after
			fmt.Fprintf(w, `<!doctype html><title>marker-%s</title><h1>%s</h1>`, marker, marker)
			return
		}
		switch r.URL.Path {
		case "/set-cookie":
			http.SetCookie(w, &http.Cookie{Name: "k-brain-e2e", Value: "real-session-42", Path: "/"})
			http.Redirect(w, r, "/", http.StatusFound)
		case "/":
			c, err := r.Cookie("k-brain-e2e")
			cookie := "none"
			if err == nil {
				cookie = c.Value
			}
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprintf(w, `<!doctype html><html><head><title>k-brain e2e</title></head><body>
<h1 id="h">hello</h1><div id="q" contenteditable="true"></div><div id="b" onclick="document.title='clicked'" style="padding:8px">go</div>
<div id="cookie">%s</div></body></html>`, cookie)
		default:
			http.NotFound(w, r)
		}
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	ip := "127.0.0.1"
	if conn, err := net.Dial("udp", "8.8.8.8:80"); err == nil {
		ip = conn.LocalAddr().(*net.UDPAddr).IP.String()
		conn.Close()
	}
	return fmt.Sprintf("http://%s:%d", ip, ln.Addr().(*net.TCPAddr).Port)
}

func TestE2EHeadless(t *testing.T) {
	_ = chromiumPath(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))

	url := testPage(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	b, err := Open(ctx, ModeHeadless)
	if err != nil {
		t.Fatalf("open headless: %v", err)
	}
	defer b.Close()
	if b.Mode() != ModeHeadless {
		t.Fatalf("mode: %v", b.Mode())
	}

	if err := b.Navigate(ctx, url+"/set-cookie"); err != nil {
		t.Fatal(err)
	}
	info, err := b.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(info.URL, url) {
		t.Fatalf("url: %q", info.URL)
	}

	cookie, err := b.Eval(ctx, `document.getElementById("cookie").textContent`)
	if err != nil {
		t.Fatal(err)
	}
	if cookie != `"real-session-42"` {
		t.Fatalf("cookie round-trip failed: %s", cookie)
	}

	tree, err := b.AXTree(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tree, "hello") {
		t.Fatalf("ax tree missing heading: %.200s", tree)
	}
	h, err := b.Eval(ctx, `(()=>{const r=document.getElementById("b").getBoundingClientRect();return [r.x+r.width/2,r.y+r.height/2]})()`)
	if err != nil {
		t.Fatal(err)
	}
	var xy [2]float64
	if err := jsonUnmarshal(h, &xy); err != nil {
		t.Fatal(err)
	}
	if err := b.ClickAt(ctx, xy[0], xy[1]); err != nil {
		t.Fatal(err)
	}
	title, err := b.Eval(ctx, `document.title`)
	if err != nil || title != `"clicked"` {
		t.Fatalf("click didn't land: title=%s err=%v", title, err)
	}

	jpeg, err := b.Screenshot(ctx, 1568)
	if err != nil {
		t.Fatal(err)
	}
	if len(jpeg) < 500 || jpeg[0] != 0xFF || jpeg[1] != 0xD8 {
		t.Fatalf("not a jpeg: %d bytes, magic %x", len(jpeg), jpeg[:2])
	}
}

func TestE2EDedicated(t *testing.T) {
	_ = chromiumPath(t)
	url := testPage(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", os.Getenv("HOME"))

	b, err := Open(ctx, ModeDedicated)
	if err != nil {
		t.Fatalf("open dedicated: %v", err)
	}
	defer b.Close()
	if err := b.Navigate(ctx, url); err != nil {
		t.Fatal(err)
	}

	if err := b.Fill(ctx, "#q", "paper towels"); err != nil {
		t.Fatal(err)
	}
	v, err := b.Eval(ctx, `document.activeElement.id`)
	if err != nil || v != `"q"` {
		t.Fatalf("fill focus: %s %v", v, err)
	}

	if _, err := os.Stat(filepath.Join(home, ".k-brain", "browser", "dedicated-profile")); err != nil {
		t.Fatalf("dedicated profile dir missing: %v", err)
	}
}

func TestE2ELiveAttach(t *testing.T) {
	bin := chromiumPath(t)
	url := testPage(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	portLn, _ := net.Listen("tcp", "127.0.0.1:0")
	port := portLn.Addr().(*net.TCPAddr).Port
	portLn.Close()
	profile := t.TempDir()
	cmd := exec.Command(bin,
		fmt.Sprintf("--remote-debugging-port=%d", port),
		"--user-data-dir="+profile,
		"--no-first-run", "--no-default-browser-check", "--headless=new",
		"about:blank",
	)
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()

	t.Setenv("K_BRAIN_CDP_URL", fmt.Sprintf("http://127.0.0.1:%d", port))
	deadline := time.Now().Add(30 * time.Second)
	var b Backend
	var err error
	for time.Now().Before(deadline) {
		b, err = Open(ctx, ModeLive)
		if err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("attach to live chrome: %v", err)
	}
	if err := b.Navigate(ctx, url+"/set-cookie"); err != nil {
		t.Fatal(err)
	}
	cookie, err := b.Eval(ctx, `document.getElementById("cookie").textContent`)
	if err != nil {
		t.Fatal(err)
	}
	if cookie != `"real-session-42"` {
		t.Fatalf("live cookie: %s", cookie)
	}

	tabs, err := b.Tabs(ctx)
	if err != nil || len(tabs) == 0 {
		t.Fatalf("tabs: %v %v", tabs, err)
	}

	b.Close()
	ws, err := DiscoverLiveWS(ctx)
	if err != nil {
		t.Fatalf("browser died with our Close: %v", err)
	}
	if !strings.Contains(ws, "devtools/browser") {
		t.Fatalf("ws url: %q", ws)
	}
}

func jsonUnmarshal(s string, v any) error {
	return json.Unmarshal([]byte(s), v)
}

func TestEvalImmediatelyAfterAttach(t *testing.T) {
	_ = chromiumPath(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for i := range 5 {
		b, err := Open(ctx, ModeHeadless)
		if err != nil {
			t.Fatalf("iter %d open: %v", i, err)
		}

		res, err := b.Eval(ctx, "1+1")
		if err != nil {
			t.Fatalf("iter %d eval: %v", i, err)
		}
		if res != "2" {
			t.Fatalf("iter %d: got %s", i, res)
		}
		b.Close()
	}
}

func TestE2ELiveFallsBackToLaunched(t *testing.T) {
	if os.Getenv("K_BRAIN_CDP_WS") != "" || os.Getenv("K_BRAIN_CDP_URL") != "" {
		t.Skip("explicit CDP endpoint set — fallback bypassed")
	}

	for _, p := range []int{9222, 9223} {
		if portLive(p) {
			t.Skipf("ambient browser on %d — fallback would not trigger", p)
		}
	}
	_ = chromiumPath(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	url := testPage(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	b, err := Open(ctx, ModeLive)
	if err != nil {
		t.Fatalf("live fallback must not error: %v", err)
	}
	defer b.Close()
	if b.Obtained() != ObtainedLaunched {
		t.Fatalf("fallback should have launched, got obtained=%v", b.Obtained())
	}
	if err := b.Navigate(ctx, url); err != nil {
		t.Fatal(err)
	}
	info, err := b.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(info.URL, url) {
		t.Fatalf("url: %q", info.URL)
	}
}

func TestE2EDedicatedReattach(t *testing.T) {
	_ = chromiumPath(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	url := testPage(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	b1, err := Open(ctx, ModeDedicated)
	if err != nil {
		t.Fatalf("launch dedicated: %v", err)
	}

	prof := dedicatedProfileDir(home, "default")
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer ccancel()
		killProfileChrome(cctx, t, prof)
	})
	if b1.Obtained() != ObtainedLaunched {
		t.Fatalf("first open should launch, got %v", b1.Obtained())
	}
	if err := b1.Navigate(ctx, url+"/set-cookie"); err != nil {
		t.Fatal(err)
	}

	b1.Close()
	if _, ok := DiscoverWSForProfile(ctx, prof); !ok {
		t.Fatal("Close must leave the dedicated Chrome alive for reattach")
	}

	b2, err := Open(ctx, ModeDedicated)
	if err != nil {
		t.Fatalf("reattach: %v", err)
	}
	if b2.Obtained() != ObtainedReattached {
		t.Fatalf("second open should reattach, got %v", b2.Obtained())
	}
	if b2.(*Browser).launcher != nil {
		t.Fatal("reattach must not own a launcher (that would be a new process)")
	}
	if err := b2.Navigate(ctx, url+"/"); err != nil {
		t.Fatal(err)
	}
	cookie, err := b2.Eval(ctx, `document.getElementById("cookie").textContent`)
	if err != nil {
		t.Fatal(err)
	}
	if cookie != `"real-session-42"` {
		t.Fatalf("reattached browser lost profile cookies: %s", cookie)
	}

	b2.Close()
	if _, ok := DiscoverWSForProfile(ctx, prof); !ok {
		t.Fatal("reattached Close must leave Chrome alive")
	}
	b3, err := Open(ctx, ModeDedicated)
	if err != nil {
		t.Fatalf("third open: %v", err)
	}
	if b3.Obtained() != ObtainedReattached {
		t.Fatalf("third open should reattach the surviving Chrome, got %v", b3.Obtained())
	}
	b3.Close()

}

func killProfileChrome(ctx context.Context, t *testing.T, prof string) {
	t.Helper()
	ws, ok := DiscoverWSForProfile(ctx, prof)
	if !ok {
		return
	}

	if b := rod.New().ControlURL(ws); b.Connect() == nil {
		_ = b.Close()
	}
	time.Sleep(500 * time.Millisecond)
}
