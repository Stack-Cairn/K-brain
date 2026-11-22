package backend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

// uiPluginFixture adds an enabled user plugin "board" with a side pane panel,
// and a workspace plugin "wsui" with a settings panel.
func uiPluginFixture(t *testing.T) (*pluginFixture, string) {
	t.Helper()
	f := newPluginFixture(t)
	dir := filepath.Join(f.home, "plugins", "board")
	writePlugin(t, dir, map[string]any{"name": "board", "enabled": true, "ui": map[string]any{
		"panels": []map[string]any{{"id": "main", "title": "Board", "entry": "index.html", "placement": "sidePane"}},
	}})
	files := map[string]string{
		"ui/index.html":       "<!doctype html><script src=\"app.js\"></script>",
		"ui/app.js":           "console.log('board')",
		"ui/assets/style.css": "body{}",
		"ui/data.bin":         "\x00\x01",
		"ui/.secret":          "hidden",
		"ui/my file.txt":      "spaced",
		"secret.txt":          "outside the ui root",
	}
	for name, body := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside the plugin"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "ui", "escape.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(dir, "ui", "escdir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../secret.txt", filepath.Join(dir, "ui", "up.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("app.js", filepath.Join(dir, "ui", "inside.js")); err != nil {
		t.Fatal(err)
	}
	big, err := os.Create(filepath.Join(dir, "ui", "big.js"))
	if err != nil {
		t.Fatal(err)
	}
	if err := big.Truncate(maxPluginAssetBytes + 1); err != nil {
		t.Fatal(err)
	}
	big.Close()
	ws := filepath.Join(f.workdir, ".liveagent", "plugins", "wsui")
	writePlugin(t, ws, map[string]any{"name": "wsui", "enabled": true, "ui": map[string]any{"root": "web",
		"panels": []map[string]any{{"id": "prefs", "title": "Prefs", "entry": "prefs.html", "placement": "settings"}},
	}})
	if err := os.MkdirAll(filepath.Join(ws, "web"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "web", "prefs.html"), []byte("<p>prefs</p>"), 0o600); err != nil {
		t.Fatal(err)
	}
	return f, dir
}

func panelURL(t *testing.T, list pluginList, id string) string {
	t.Helper()
	p := pluginByID(list, id)
	if p == nil || len(p.Contributes.UI) == 0 {
		t.Fatalf("plugin %s has no panels: %+v", id, list)
	}
	return p.Contributes.UI[0].URL
}

// fetchAsset requests an asset without the bearer token, as a frame would.
func (f *pluginFixture) fetchAsset(method, path string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	r.Host = "127.0.0.1:4100"
	w := httptest.NewRecorder()
	f.server.ServeHTTP(w, r)
	return w
}

func TestPluginPanelAssetsServeSandboxed(t *testing.T) {
	f, _ := uiPluginFixture(t)
	list := f.request(t, http.MethodGet, "/v1/plugins", "", http.StatusOK)
	board := pluginByID(list, "board")
	if board == nil || len(board.Contributes.UI) != 1 {
		t.Fatalf("board = %+v", board)
	}
	panel := board.Contributes.UI[0]
	if panel.ID != "main" || panel.Title != "Board" || panel.Placement != "sidePane" || panel.Entry != "index.html" ||
		!strings.HasPrefix(panel.URL, pluginAssetPrefix) || !strings.HasSuffix(panel.URL, "/board/index.html") {
		t.Fatalf("panel = %+v", panel)
	}
	base := strings.TrimSuffix(panel.URL, "index.html")
	w := f.fetchAsset(http.MethodGet, panel.URL)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "app.js") {
		t.Fatalf("entry: %d %s", w.Code, w.Body.String())
	}
	h := w.Header()
	csp := h.Get("Content-Security-Policy")
	if h.Get("Content-Type") != "text/html; charset=utf-8" || !strings.HasPrefix(csp, "sandbox allow-scripts;") ||
		strings.Contains(csp, "allow-same-origin") || !strings.Contains(csp, "connect-src 'none'") ||
		!strings.Contains(csp, "script-src http://127.0.0.1:4100"+base+" 'unsafe-inline'") ||
		h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Cache-Control") != "no-store" ||
		h.Get("Referrer-Policy") != "no-referrer" || h.Get("Set-Cookie") != "" {
		t.Fatalf("headers = %v", h)
	}
	for path, want := range map[string]string{
		"app.js":           "text/javascript; charset=utf-8",
		"assets/style.css": "text/css; charset=utf-8",
		"data.bin":         "application/octet-stream",
		"inside.js":        "text/javascript; charset=utf-8",
		"my%20file.txt":    "text/plain; charset=utf-8",
	} {
		w := f.fetchAsset(http.MethodGet, base+path)
		if w.Code != http.StatusOK || w.Header().Get("Content-Type") != want || !strings.HasPrefix(w.Header().Get("Content-Security-Policy"), "sandbox allow-scripts;") {
			t.Errorf("%s: %d %q", path, w.Code, w.Header().Get("Content-Type"))
		}
	}
	if w := f.fetchAsset(http.MethodHead, base+"app.js"); w.Code != http.StatusOK || w.Body.Len() != 0 {
		t.Errorf("HEAD: %d %q", w.Code, w.Body.String())
	}
	if w := f.fetchAsset(http.MethodPost, base+"app.js"); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d", w.Code)
	}
	if w := f.fetchAsset(http.MethodGet, base+"big.js"); w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized: %d", w.Code)
	}
	grant := strings.Split(strings.TrimPrefix(base, pluginAssetPrefix), "/")[0]
	for _, path := range []string{
		base + "../plugin.json",
		base + "%2e%2e/secret.txt",
		base + "..%2Fsecret.txt",
		base + "assets%2F..%2F..%2Fsecret.txt",
		base + "assets/../../secret.txt",
		base + ".secret",
		base + "escape.txt",
		base + "escdir/outside.txt",
		base + "up.txt",
		base + "assets",
		base + "assets/",
		base + "missing.js",
		base + "a%5C..%5Csecret.txt",
		pluginAssetPrefix + grant + "/notes/index.html",
		pluginAssetPrefix + strings.Repeat("0", 32) + "/board/index.html",
		pluginAssetPrefix + grant + "/board",
	} {
		if w := f.fetchAsset(http.MethodGet, path); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	// Disabling the plugin withdraws its URL and its assets.
	list = f.request(t, http.MethodPost, "/v1/plugins/board/disable", "", http.StatusOK)
	if p := pluginByID(list, "board"); p.Contributes.UI[0].URL != "" {
		t.Fatalf("disabled plugin kept a panel URL: %+v", p)
	}
	if w := f.fetchAsset(http.MethodGet, panel.URL); w.Code != http.StatusNotFound {
		t.Fatalf("asset of a disabled plugin: %d", w.Code)
	}
}

func TestWorkspacePluginPanelsFollowTrust(t *testing.T) {
	f, _ := uiPluginFixture(t)
	query := "/v1/plugins?workdir=" + url.QueryEscape(f.workdir)
	if pluginByID(f.request(t, http.MethodGet, query, "", http.StatusOK), "wsui") != nil {
		t.Fatal("untrusted workspace plugin listed")
	}
	if err := config.Trust(f.workdir); err != nil {
		t.Fatal(err)
	}
	list := f.request(t, http.MethodGet, query, "", http.StatusOK)
	panelPath := panelURL(t, list, "wsui")
	if p := pluginByID(list, "wsui"); p.Contributes.UI[0].Placement != "settings" {
		t.Fatalf("panel = %+v", p.Contributes.UI)
	}
	if w := f.fetchAsset(http.MethodGet, panelPath); w.Code != http.StatusOK || w.Body.String() != "<p>prefs</p>" {
		t.Fatalf("trusted asset: %d %s", w.Code, w.Body.String())
	}
	if err := config.Untrust(f.workdir); err != nil {
		t.Fatal(err)
	}
	if w := f.fetchAsset(http.MethodGet, panelPath); w.Code != http.StatusNotFound {
		t.Fatalf("asset after trust was revoked: %d", w.Code)
	}
}

func TestPluginToolCallRouteRunsOnlyThePluginsOwnTools(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fixture plugin is a shell script")
	}
	f := newPluginFixture(t)
	dir := filepath.Join(f.home, "plugins", "echo")
	writePlugin(t, dir, map[string]any{"name": "echo", "command": []string{"./run.sh"}, "enabled": true, "tools": []map[string]any{{"name": "ping"}}})
	script := "#!/bin/sh\nread line\necho '{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"content\":\"pong\"}}'\n"
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	call := func(path, body string, status int) map[string]any {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer plugin-token")
		w := httptest.NewRecorder()
		f.server.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	if out := call("/v1/plugins/echo/tools/call", `{"name":"ping","arguments":{}}`, http.StatusOK); out["output"] != "pong" || out["failed"] != false {
		t.Fatalf("call = %v", out)
	}
	// Another plugin's tool through this plugin's route, an unknown tool and
	// a disabled plugin are all refused.
	call("/v1/plugins/notes/tools/call", `{"name":"echo_ping"}`, http.StatusNotFound)
	call("/v1/plugins/echo/tools/call", `{"name":"missing"}`, http.StatusNotFound)
	call("/v1/plugins/echo/tools/call", `{}`, http.StatusBadRequest)
	f.request(t, http.MethodPost, "/v1/plugins/echo/disable", "", http.StatusOK)
	call("/v1/plugins/echo/tools/call", `{"name":"ping"}`, http.StatusNotFound)
	// Unauthenticated callers are refused like every other route.
	r := httptest.NewRequest(http.MethodPost, "/v1/plugins/echo/tools/call", strings.NewReader(`{"name":"ping"}`))
	w := httptest.NewRecorder()
	f.server.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated call: %d", w.Code)
	}
}
