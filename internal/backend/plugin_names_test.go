package backend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/plugins"
)

// Plugins whose id has "_" get unambiguous tool names; the list pairs each
// model-facing name with the plugin's own, and the call route takes either.
func TestPluginToolNamesForIDsWithUnderscores(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fixture plugin is a shell script")
	}
	f := newPluginFixture(t)
	dir := filepath.Join(f.home, "plugins", "my_echo")
	writePlugin(t, dir, map[string]any{"name": "my_echo", "command": []string{"./run.sh"}, "enabled": true, "tools": []map[string]any{{"name": "ping_now", "description": "Ping."}}})
	script := "#!/bin/sh\nread line\necho '{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"content\":\"pong\"}}'\n"
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	list := f.request(t, http.MethodGet, "/v1/plugins", "", http.StatusOK)
	modelName := plugins.ToolName("my_echo", "ping_now")
	p := pluginByID(list, "my_echo")
	if p == nil || len(p.Contributes.Tools) != 1 || p.Contributes.Tools[0] != modelName || !strings.HasPrefix(modelName, "plugin_my-echo-") ||
		len(p.Contributes.ToolDetails) != 1 || p.Contributes.ToolDetails[0] != (pluginToolView{Name: "ping_now", ModelName: modelName, Description: "Ping."}) {
		t.Fatalf("plugin = %+v", p)
	}
	if notes := pluginByID(list, "notes"); notes.Contributes.Tools[0] != "plugin_notes_add" || notes.Contributes.ToolDetails[0].Name != "add" {
		t.Fatalf("notes = %+v", notes.Contributes)
	}
	// The session tool runs under its model-facing name.
	var found bool
	for _, tool := range f.server.plugins.Tools("") {
		found = found || tool.Def.Function.Name == modelName
	}
	if !found {
		t.Fatalf("session tools lack %s", modelName)
	}
	call := func(body string) map[string]any {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/v1/plugins/my_echo/tools/call", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer plugin-token")
		w := httptest.NewRecorder()
		f.server.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body.String())
		}
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	for _, name := range []string{"ping_now", modelName} {
		if out := call(`{"name":"` + name + `"}`); out["output"] != "pong" || out["failed"] != false || out["name"] != modelName || out["toolName"] != "ping_now" {
			t.Fatalf("call %s = %v", name, out)
		}
	}
	// Another plugin's model-facing name is not this plugin's tool.
	r := httptest.NewRequest(http.MethodPost, "/v1/plugins/my_echo/tools/call", strings.NewReader(`{"name":"plugin_notes_add"}`))
	r.Header.Set("Authorization", "Bearer plugin-token")
	w := httptest.NewRecorder()
	f.server.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-plugin call: %d", w.Code)
	}
}

// A panel whose entry or ui root is missing is reported in the list, and the
// asset route answers a failed page with a page that tells the host.
func TestPluginPanelErrorsAreReported(t *testing.T) {
	f, dir := uiPluginFixture(t)
	writePlugin(t, filepath.Join(f.home, "plugins", "broken"), map[string]any{"name": "broken", "enabled": true, "ui": map[string]any{
		"panels": []map[string]any{{"id": "gone", "title": "Gone", "entry": "missing.html", "placement": "sidePane"}},
	}})
	writePlugin(t, filepath.Join(f.home, "plugins", "rootless"), map[string]any{"name": "rootless", "enabled": true, "ui": map[string]any{
		"root": "web", "panels": []map[string]any{{"id": "p", "title": "P", "entry": "index.html", "placement": "settings"}},
	}})
	if err := os.MkdirAll(filepath.Join(f.home, "plugins", "broken", "ui"), 0o700); err != nil {
		t.Fatal(err)
	}
	list := f.request(t, http.MethodGet, "/v1/plugins", "", http.StatusOK)
	if board := pluginByID(list, "board").Contributes.UI[0]; board.Error != "" || board.ErrorCode != "" || board.URL == "" {
		t.Fatalf("healthy panel = %+v", board)
	}
	broken := pluginByID(list, "broken").Contributes.UI[0]
	if broken.ErrorCode != panelErrorEntryMissing || !strings.Contains(broken.Error, "missing.html") || broken.URL == "" {
		t.Fatalf("broken panel = %+v", broken)
	}
	if rootless := pluginByID(list, "rootless").Contributes.UI[0]; rootless.ErrorCode != panelErrorUIRootMissing {
		t.Fatalf("rootless panel = %+v", rootless)
	}
	if err := os.Remove(filepath.Join(dir, "ui", "index.html")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "ui", "index.html"), 0o700); err != nil {
		t.Fatal(err)
	}
	list = f.request(t, http.MethodGet, "/v1/plugins", "", http.StatusOK)
	if board := pluginByID(list, "board").Contributes.UI[0]; board.ErrorCode != panelErrorEntryNotFile {
		t.Fatalf("directory entry = %+v", board)
	}

	// The asset route: a missing page gets a page that posts host.panelLoadError.
	w := f.fetchAsset(http.MethodGet, broken.URL)
	body := w.Body.String()
	if w.Code != http.StatusNotFound || w.Header().Get(pluginAssetErrorHeader) != "asset_not_found" || w.Header().Get("Content-Type") != "text/html; charset=utf-8" ||
		!strings.HasPrefix(w.Header().Get("Content-Security-Policy"), "sandbox allow-scripts;") || strings.Contains(w.Header().Get("Content-Security-Policy"), "allow-same-origin") ||
		!strings.Contains(body, `"method":"host.panelLoadError"`) || !strings.Contains(body, `"code":"asset_not_found"`) || !strings.Contains(body, `"status":404`) {
		t.Fatalf("missing page: %d %v %s", w.Code, w.Header(), body)
	}
	// Request text cannot break out of the page.
	base := strings.TrimSuffix(broken.URL, "missing.html")
	w = f.fetchAsset(http.MethodGet, base+"%3C%2Fscript%3E%3Cimg%3E.html")
	if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "</script><img>") || strings.Contains(w.Body.String(), "<img>") {
		t.Fatalf("escaped page: %s", w.Body.String())
	}
	// Other files get text; a stale grant says the plugin is unavailable.
	w = f.fetchAsset(http.MethodGet, base+"missing.js")
	if w.Code != http.StatusNotFound || w.Header().Get(pluginAssetErrorHeader) != "asset_not_found" || strings.Contains(w.Body.String(), "<script>") {
		t.Fatalf("missing script: %d %v %s", w.Code, w.Header(), w.Body.String())
	}
	f.request(t, http.MethodPost, "/v1/plugins/broken/disable", "", http.StatusOK)
	w = f.fetchAsset(http.MethodGet, broken.URL)
	if w.Code != http.StatusNotFound || w.Header().Get(pluginAssetErrorHeader) != "plugin_unavailable" || !strings.Contains(w.Body.String(), `"code":"plugin_unavailable"`) {
		t.Fatalf("disabled plugin page: %d %v %s", w.Code, w.Header(), w.Body.String())
	}
	if w := f.fetchAsset(http.MethodHead, broken.URL); w.Code != http.StatusNotFound || w.Body.Len() != 0 {
		t.Fatalf("HEAD: %d %q", w.Code, w.Body.String())
	}
	list = f.request(t, http.MethodGet, "/v1/plugins", "", http.StatusOK)
	if disabled := pluginByID(list, "broken").Contributes.UI[0]; disabled.URL != "" || disabled.ErrorCode != "" {
		t.Fatalf("disabled panel = %+v", disabled)
	}
}
