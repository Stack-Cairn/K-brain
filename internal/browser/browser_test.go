package browser

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func noEnvCDP(t *testing.T) {
	t.Helper()
	if os.Getenv("K_BRAIN_CDP_WS") != "" || os.Getenv("K_BRAIN_CDP_URL") != "" {
		t.Skip("explicit CDP endpoint set — profile scan bypassed")
	}
}

func TestParseDevToolsActivePort(t *testing.T) {
	port, path, err := parseDevToolsActivePort([]byte("9222\n/devtools/browser/abc-123\n"))
	if err != nil || port != 9222 || path != "/devtools/browser/abc-123" {
		t.Fatalf("got %d %q %v", port, path, err)
	}
	if _, _, err := parseDevToolsActivePort([]byte("9222\n")); err == nil {
		t.Fatal("one line must fail")
	}
	if _, _, err := parseDevToolsActivePort([]byte("notaport\n/x")); err == nil {
		t.Fatal("non-numeric port must fail")
	}
}

func TestProfileScanFindsPortFile(t *testing.T) {
	noEnvCDP(t)
	if os.Getenv("K_BRAIN_CDP_WS") != "" || os.Getenv("K_BRAIN_CDP_URL") != "" {
		t.Skip("explicit CDP endpoint set — profile scan bypassed")
	}
	for _, p := range []int{9222, 9223} {
		if portLive(p) {
			t.Skipf("a real browser is listening on %d", p)
		}
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	profiles := profileDirs()
	if len(profiles) == 0 {
		t.Skip("no Chromium profile locations for this platform")
	}
	prof := profiles[0]
	if err := os.MkdirAll(prof, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(prof, "DevToolsActivePort"), []byte("1\n/devtools/browser/dead\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := DiscoverLiveWS(context.Background())
	if err == nil || !strings.Contains(err.Error(), "stale DevToolsActivePort") {
		t.Fatalf("want stale-file error, got %v", err)
	}
}

func TestDiscoverViaJSONVersion(t *testing.T) {
	noEnvCDP(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/json/version" {
			fmt.Fprintf(w, `{"Browser":"HeadlessChrome/150.0","webSocketDebuggerUrl":"ws://127.0.0.1:%d/devtools/browser/xyz"}`, port)
			return
		}
		http.NotFound(w, r)
	})}
	go srv.Serve(ln)
	defer srv.Close()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	prof := filepath.Join(home, ".config", "chromium")
	if err := os.MkdirAll(prof, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(fmt.Sprintf("testhost-%d", os.Getpid()), filepath.Join(prof, "SingletonLock")); err != nil {
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation requires privileges on Windows")
		}
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prof, "DevToolsActivePort"),
		[]byte(strconv.Itoa(port)+"\n/devtools/browser/xyz\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ws, _, err := discoverProfileWS(context.Background(), prof, true)
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("ws://127.0.0.1:%d/devtools/browser/xyz", port); ws != want {
		t.Fatalf("got %q want %q", ws, want)
	}
}

func TestDiscoverFallsBackToFileWSPath(t *testing.T) {
	noEnvCDP(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})}
	go srv.Serve(ln)
	defer srv.Close()
	prof := t.TempDir()
	os.Symlink(fmt.Sprintf("h-%d", os.Getpid()), filepath.Join(prof, "SingletonLock"))
	os.WriteFile(filepath.Join(prof, "DevToolsActivePort"),
		[]byte(strconv.Itoa(port)+"\n/devtools/browser/fromfile\n"), 0o644)
	ws, _, err := discoverProfileWS(context.Background(), prof, true)
	if err != nil {
		t.Fatalf("file ws path must be offered as a candidate, got err=%v", err)
	}
	if want := fmt.Sprintf("ws://127.0.0.1:%d/devtools/browser/fromfile", port); ws != want {
		t.Fatalf("got %q want %q", ws, want)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := connectLive(ctx, ws); !errors.Is(err, errLiveRefused) {
		t.Fatalf("404-only endpoint must be refused at connect, got %v", err)
	}
	// k-brain's own dedicated profile is still probed before reattaching.
	if ws, _, err := discoverProfileWS(context.Background(), prof, false); err == nil || ws != "" {
		t.Fatalf("404-only endpoint must be refused for dedicated reattach, got ws=%q err=%v", ws, err)
	}

	ln2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port2 := ln2.Addr().(*net.TCPAddr).Port
	srv2 := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/devtools/browser/") {

			hj, ok := w.(http.Hijacker)
			if !ok {
				http.Error(w, "no hijack", http.StatusInternalServerError)
				return
			}
			conn, buf, err := hj.Hijack()
			if err != nil {
				return
			}
			defer conn.Close()
			fmt.Fprintf(buf, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: s3pPLMBiTxaQ9kYGzzhZRbK+xOo=\r\n\r\n")
			buf.Flush()
			return
		}
		http.NotFound(w, r)
	})}
	go srv2.Serve(ln2)
	defer srv2.Close()
	prof2 := t.TempDir()
	os.Symlink(fmt.Sprintf("h-%d", os.Getpid()), filepath.Join(prof2, "SingletonLock"))
	os.WriteFile(filepath.Join(prof2, "DevToolsActivePort"),
		[]byte(strconv.Itoa(port2)+"\n/devtools/browser/fromfile\n"), 0o644)
	ws, _, err = discoverProfileWS(context.Background(), prof2, false)
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("ws://127.0.0.1:%d/devtools/browser/fromfile", port2); ws != want {
		t.Fatalf("got %q want %q", ws, want)
	}
}

func TestDiscoverPermissionBlocked(t *testing.T) {
	noEnvCDP(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})}
	go srv.Serve(ln)
	defer srv.Close()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	prof := filepath.Join(home, ".config", "google-chrome")
	os.MkdirAll(prof, 0o755)
	os.Symlink(fmt.Sprintf("h-%d", os.Getpid()), filepath.Join(prof, "SingletonLock"))
	os.WriteFile(filepath.Join(prof, "DevToolsActivePort"), []byte(strconv.Itoa(port)+"\n/devtools/browser/x\n"), 0o644)

	// /json/version is forbidden until the user approves, but the file names
	// the websocket: offer it so the real connection can wait for Allow.
	ws, _, err := discoverProfileWS(context.Background(), prof, true)
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("ws://127.0.0.1:%d/devtools/browser/x", port); ws != want {
		t.Fatalf("got %q want %q", ws, want)
	}

	// Without a file path there is nothing to connect to, so the 403 surfaces.
	if _, err := resolveWSURL(context.Background(), "", "", port, "", true); err == nil || !strings.Contains(err.Error(), "permission-blocked") {
		t.Fatalf("want permission-blocked, got %v", err)
	}
}

func TestDiscoverExplicitEndpoints(t *testing.T) {
	t.Setenv("K_BRAIN_CDP_WS", "ws://example:1234/devtools/browser/explicit")
	ws, err := DiscoverLiveWS(context.Background())
	if err != nil || ws != "ws://example:1234/devtools/browser/explicit" {
		t.Fatalf("K_BRAIN_CDP_WS: got %q %v", ws, err)
	}
}

func TestCheckURLFloor(t *testing.T) {
	ctx := context.Background()
	for _, u := range []string{
		"http://169.254.169.254/latest/meta-data",
		"http://metadata.google.internal/",
		"http://169.254.170.2/v2/metadata",
	} {
		if err := CheckURL(ctx, u); err == nil {
			t.Errorf("%s must be blocked", u)
		}
	}
	if err := CheckURL(ctx, "https://example.com/"); err != nil {
		t.Errorf("example.com must pass: %v", err)
	}
	if err := CheckURL(ctx, "chrome://newtab"); err != nil {
		t.Errorf("non-http schemes pass: %v", err)
	}
}

func TestCheckPrivateURL(t *testing.T) {
	ctx := context.Background()
	for _, u := range []string{
		"http://127.0.0.1:8080/",
		"http://10.0.0.5/",
		"http://192.168.1.1/",
		"http://100.64.1.2/",
		"http://[::1]/",
	} {
		if err := CheckPrivateURL(ctx, u); err == nil {
			t.Errorf("%s must be blocked", u)
		}
	}
	if err := CheckPrivateURL(ctx, "https://example.com/"); err != nil {
		t.Errorf("example.com must pass: %v", err)
	}
}

func TestSessionModeSelection(t *testing.T) {
	m := NewManager(ModeLive)
	s, err := m.Session("")
	if err != nil || s.name != "default" || s.mode != ModeLive {
		t.Fatalf("default session: %+v %v", s, err)
	}
	s, err = m.Session("headless:scrape")
	if err != nil || s.name != "scrape" || s.mode != ModeHeadless {
		t.Fatalf("mode-prefixed session: %+v %v", s, err)
	}
	if _, err := m.Session("bogus-mode:x"); err == nil {
		t.Fatal("unknown mode prefix must fail")
	}
	if _, err = m.Session("../evil"); err == nil {
		t.Fatal("path-traversal name must fail")
	}

	a, _ := m.Session("work")
	b, _ := m.Session("work")
	if a != b {
		t.Fatal("same name must return the same session")
	}
	c, _ := m.Session("dedicated:work")
	if c == a {
		t.Fatal("different modes must not share a session")
	}
}

func TestPortLiveDualStack(t *testing.T) {
	ln, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skipf("no IPv6 loopback here: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	if !portLive(port) {
		t.Fatalf("portLive missed a [::1]-only listener on %d", port)
	}
}

func TestSquatterRejected(t *testing.T) {
	noEnvCDP(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})}
	go srv.Serve(ln)
	defer srv.Close()

	if ws, err := resolveWSURL(context.Background(), "", "", port, "", true); err == nil || ws != "" {
		t.Fatalf("squatter must not resolve: ws=%q err=%v", ws, err)
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	prof := filepath.Join(home, ".config", "google-chrome")
	os.MkdirAll(prof, 0o755)
	os.Symlink(fmt.Sprintf("h-%d", os.Getpid()), filepath.Join(prof, "SingletonLock"))
	os.WriteFile(filepath.Join(prof, "DevToolsActivePort"),
		[]byte(strconv.Itoa(port)+"\n/devtools/browser/dead\n"), 0o644)
	if ws, ok := DiscoverWSForProfile(context.Background(), prof); ok || ws != "" {
		t.Fatalf("squatter behind profile file must not reattach: %q", ws)
	}
}

func TestDiscoverWSForProfileLive(t *testing.T) {
	noEnvCDP(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/json/version" {
			fmt.Fprintf(w, `{"Browser":"HeadlessChrome/150.0","webSocketDebuggerUrl":"ws://127.0.0.1:%d/devtools/browser/live"}`, port)
			return
		}
		http.NotFound(w, r)
	})}
	go srv.Serve(ln)
	defer srv.Close()

	prof := t.TempDir()
	os.Symlink(fmt.Sprintf("h-%d", os.Getpid()), filepath.Join(prof, "SingletonLock"))
	os.WriteFile(filepath.Join(prof, "DevToolsActivePort"),
		[]byte(strconv.Itoa(port)+"\n/devtools/browser/live\n"), 0o644)
	ws, ok := DiscoverWSForProfile(context.Background(), prof)
	if !ok || !strings.Contains(ws, "/devtools/browser/live") {
		t.Fatalf("reattach discovery: %q ok=%v", ws, ok)
	}

	if _, ok := DiscoverWSForProfile(context.Background(), filepath.Join(prof, "nope")); ok {
		t.Fatal("missing profile must not reattach")
	}
}

type fakeBackend struct {
	mode     Mode
	obtained Obtained
	closed   bool
}

func (f *fakeBackend) Prepare(context.Context) error                   { return nil }
func (f *fakeBackend) Invalidate(error)                                {}
func (f *fakeBackend) Info(context.Context) (PageInfo, error)          { return PageInfo{}, nil }
func (f *fakeBackend) Navigate(context.Context, string) error          { return nil }
func (f *fakeBackend) Back(context.Context) error                      { return nil }
func (f *fakeBackend) Eval(context.Context, string) (string, error)    { return "null", nil }
func (f *fakeBackend) ClickAt(context.Context, float64, float64) error { return nil }
func (f *fakeBackend) TypeText(context.Context, string) error          { return nil }
func (f *fakeBackend) PressKey(context.Context, string) error          { return nil }
func (f *fakeBackend) Fill(context.Context, string, string) error      { return nil }
func (f *fakeBackend) Scroll(context.Context, float64) error           { return nil }
func (f *fakeBackend) WaitLoad(context.Context) error                  { return nil }
func (f *fakeBackend) WaitElement(context.Context, string, bool) (bool, error) {
	return true, nil
}
func (f *fakeBackend) Screenshot(context.Context, int) ([]byte, error) { return nil, nil }
func (f *fakeBackend) AXTree(context.Context) (string, error)          { return "[]", nil }
func (f *fakeBackend) BoxModel(context.Context, int) (float64, float64, error) {
	return 0, 0, nil
}
func (f *fakeBackend) Tabs(context.Context) ([]Tab, error)  { return nil, nil }
func (f *fakeBackend) UseTab(context.Context, string) error { return nil }
func (f *fakeBackend) UploadFiles(context.Context, string, []string) error {
	return nil
}
func (f *fakeBackend) HandleDialog(bool, string) error { return nil }
func (f *fakeBackend) Mode() Mode                      { return f.mode }
func (f *fakeBackend) Obtained() Obtained              { return f.obtained }
func (f *fakeBackend) Close() error                    { f.closed = true; return nil }

func stubOpen(t *testing.T, fn func(context.Context, Mode, string) (Backend, error)) {
	t.Helper()
	old := openNamed
	openNamed = fn
	t.Cleanup(func() { openNamed = old })
}

func TestSessionModeAndNameIsolation(t *testing.T) {
	m := NewManager(ModeLive)
	live, _ := m.Session("shared")
	same, _ := m.Session("live:shared")
	dedicated, _ := m.Session("dedicated:shared")
	other, _ := m.Session("other")
	if live != same || live == dedicated || live == other {
		t.Fatal("session mode/name keys are not isolated")
	}
	b := &fakeBackend{mode: ModeLive, obtained: ObtainedLive}
	stubOpen(t, func(context.Context, Mode, string) (Backend, error) { return b, nil })
	if _, err := live.Do(t.Context(), func(Backend) (string, error) { return "first", nil }); err != nil {
		t.Fatal(err)
	}
	live.drop()
	if !b.closed || dedicated.backend != nil || other.backend != nil {
		t.Fatal("closing one session changed another")
	}
}

func TestPortProbeSquatterTriggersFallback(t *testing.T) {
	if os.Getenv("K_BRAIN_SKIP_PORT_SQUATTER_TEST") == "1" {

		t.Skip("skipped via K_BRAIN_SKIP_PORT_SQUATTER_TEST")
	}

	var lns []net.Listener
	for _, p := range []int{9222, 9223} {
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", p))
		if err != nil {
			continue
		}
		lns = append(lns, ln)
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		})}
		go srv.Serve(ln)
		defer srv.Close()
	}
	if len(lns) == 0 {
		t.Skip("could not bind 9222/9223")
	}
	noEnvCDP(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))

	ws, err := DiscoverLiveWS(context.Background())
	if err == nil {
		t.Fatalf("squatter must not resolve to a ws url: %q", ws)
	}
	if !errors.Is(err, ErrNoLiveBrowser) {
		t.Fatalf("want ErrNoLiveBrowser (triggers fallback), got: %v", err)
	}
}
