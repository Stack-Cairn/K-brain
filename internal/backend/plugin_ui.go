package backend

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/plugins"
)

// Plugin UI panels are static files a client renders in a sandboxed frame. A
// frame cannot send the backend's bearer token, so panel files are served
// without it under a capability URL:
//
//	/v1/plugin-assets/{grant}/{plugin}/{path}
//
// grant is an HMAC of the plugin's name and directory under a key that lives
// only in this process; it is handed out by the authenticated GET
// /v1/plugins, and stops working when the backend restarts, the plugin is
// disabled or moved, or the folder holding a workspace plugin loses trust.

const (
	pluginAssetPrefix = "/v1/plugin-assets/"
	// maxPluginAssetBytes bounds one served file.
	maxPluginAssetBytes = 10 << 20
)

// pluginGrant is what a grant in an asset URL stands for.
type pluginGrant struct {
	project, id, dir string
}

// grantLocked returns p's grant and remembers what it stands for; h.mu is held.
func (h *PluginHost) grantLocked(project string, p plugins.Plugin) string {
	mac := hmac.New(sha256.New, h.secret[:])
	mac.Write([]byte("plugin-ui\x00" + project + "\x00" + p.Name + "\x00" + p.Dir))
	grant := hex.EncodeToString(mac.Sum(nil))[:32]
	h.grants[grant] = pluginGrant{project: project, id: p.Name, dir: p.Dir}
	return grant
}

// pluginPanelView is one entry of contributes.ui.
type pluginPanelView struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Placement string `json:"placement"`
	Entry     string `json:"entry"`
	// URL loads the panel; a path on this backend, present only while the
	// plugin is enabled.
	URL string `json:"url,omitempty"`
}

// panelViewsLocked lists p's panels; h.mu is held.
func (h *PluginHost) panelViewsLocked(project string, p plugins.Plugin) []pluginPanelView {
	out := []pluginPanelView{}
	if p.UI == nil {
		return out
	}
	grant := ""
	if p.Enabled && len(p.UI.Panels) > 0 {
		grant = h.grantLocked(project, p)
	}
	for _, panel := range p.UI.Panels {
		view := pluginPanelView{ID: panel.ID, Title: panel.Title, Placement: panel.Placement, Entry: panel.Entry}
		if grant != "" {
			view.URL = pluginAssetPrefix + grant + "/" + p.Name + "/" + panel.Entry
		}
		out = append(out, view)
	}
	return out
}

var errPluginAssetGone = errors.New("plugin asset not found")

// openPluginAsset opens rel under the plugin's UI root after checking that
// the grant still stands for an enabled plugin at the same place, in a folder
// that is still trusted when it is a workspace plugin.
func (h *PluginHost) openPluginAsset(grant, id, rel string) (*os.File, fs.FileInfo, error) {
	h.mu.Lock()
	g, ok := h.grants[grant]
	m := h.managers[g.project]
	h.mu.Unlock()
	if !ok || g.id != id || m == nil || !plugins.CleanRelativePath(rel) {
		return nil, nil, errPluginAssetGone
	}
	if g.project != "" && !config.Trusted(g.project) {
		return nil, nil, errPluginAssetGone
	}
	p, found := m.Get(id)
	if !found || !p.Enabled || p.Dir != g.dir || p.UI == nil {
		return nil, nil, errPluginAssetGone
	}
	// os.Root keeps every step, symbolic links included, inside the plugin
	// directory and then inside its UI root.
	pluginRoot, err := os.OpenRoot(p.Dir)
	if err != nil {
		return nil, nil, errPluginAssetGone
	}
	defer pluginRoot.Close()
	uiRoot, err := pluginRoot.OpenRoot(p.UIRoot())
	if err != nil {
		return nil, nil, errPluginAssetGone
	}
	defer uiRoot.Close()
	file, err := uiRoot.Open(rel)
	if err != nil {
		return nil, nil, errPluginAssetGone
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, nil, errPluginAssetGone
	}
	return file, info, nil
}

// assetContentTypes are the types plugin and preview files are served as;
// anything else is application/octet-stream.
var assetContentTypes = map[string]string{
	".html":  "text/html; charset=utf-8",
	".htm":   "text/html; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".mjs":   "text/javascript; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".json":  "application/json",
	".map":   "application/json",
	".txt":   "text/plain; charset=utf-8",
	".svg":   "image/svg+xml",
	".png":   "image/png",
	".jpg":   "image/jpeg",
	".jpeg":  "image/jpeg",
	".gif":   "image/gif",
	".webp":  "image/webp",
	".avif":  "image/avif",
	".ico":   "image/vnd.microsoft.icon",
	".woff":  "font/woff",
	".woff2": "font/woff2",
	".ttf":   "font/ttf",
	".otf":   "font/otf",
	".mp3":   "audio/mpeg",
	".wav":   "audio/wav",
	".mp4":   "video/mp4",
	".webm":  "video/webm",
	".wasm":  "application/wasm",
}

func assetContentType(name string) string {
	if t, ok := assetContentTypes[strings.ToLower(path.Ext(name))]; ok {
		return t
	}
	return "application/octet-stream"
}

// setSandboxHeaders sets the headers every sandboxed page and asset is served
// with: a CSP sandbox without allow-same-origin (the document gets an opaque
// origin, so it cannot read the backend's other responses, storage or
// cookies), the given resource policy, and no caching, sniffing or referrer.
func setSandboxHeaders(w http.ResponseWriter, contentType, policy string) {
	header := w.Header()
	header.Set("Content-Type", contentType)
	header.Set("Content-Security-Policy", policy)
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Cache-Control", "no-store")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("Cross-Origin-Resource-Policy", "cross-origin")
	header.Del("Set-Cookie")
}

// pluginAssetPolicy lets a panel load scripts, styles, images, fonts and media
// from its own plugin's assets (base) and inline, and nothing else: no
// network access, no nested frames, no form posts.
func pluginAssetPolicy(base string) string {
	return "sandbox allow-scripts; default-src 'none'; " +
		"script-src " + base + " 'unsafe-inline'; style-src " + base + " 'unsafe-inline'; " +
		"img-src " + base + " data: blob:; font-src " + base + " data:; media-src " + base + " data: blob:; " +
		"connect-src 'none'; frame-src 'none'; worker-src 'none'; form-action 'none'; base-uri 'none'; object-src 'none'"
}

// requestBase is the scheme and host the request was sent to, for CSP source
// expressions; "'self'" when the host is not a plain host[:port].
func requestBase(r *http.Request) string {
	host := r.Host
	if host == "" {
		return "'self'"
	}
	for _, c := range host {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune(".-:[]", c)) {
			return "'self'"
		}
	}
	if r.TLS != nil {
		return "https://" + host
	}
	return "http://" + host
}

// servePluginAsset serves GET/HEAD /v1/plugin-assets/{grant}/{plugin}/{path}.
// It runs before the bearer token check (see the package comment above).
func (s *Server) servePluginAsset(w http.ResponseWriter, r *http.Request) {
	notFound := func() {
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "not found", http.StatusNotFound)
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.plugins == nil {
		notFound()
		return
	}
	// Split the path as sent, so an encoded slash cannot join two segments.
	var segments []string
	for _, raw := range strings.Split(strings.TrimPrefix(r.URL.EscapedPath(), pluginAssetPrefix), "/") {
		segment, err := url.PathUnescape(raw)
		if err != nil || strings.Contains(segment, "/") {
			notFound()
			return
		}
		segments = append(segments, segment)
	}
	if len(segments) < 3 || len(segments[0]) != 32 {
		notFound()
		return
	}
	grant, id, rel := segments[0], segments[1], strings.Join(segments[2:], "/")
	file, info, err := s.plugins.openPluginAsset(grant, id, rel)
	if err != nil {
		notFound()
		return
	}
	defer file.Close()
	if info.Size() > maxPluginAssetBytes {
		http.Error(w, "plugin asset too large", http.StatusRequestEntityTooLarge)
		return
	}
	setSandboxHeaders(w, assetContentType(rel), pluginAssetPolicy(requestBase(r)+pluginAssetPrefix+grant+"/"+id+"/"))
	http.ServeContent(w, r, "", time.Time{}, file)
}
