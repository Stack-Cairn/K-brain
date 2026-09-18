package extrelay

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

type frame struct {
	ID        int64           `json:"id,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     json.RawMessage `json:"error,omitempty"`
}

type Relay struct {
	token   string
	ln      net.Listener
	mu      sync.Mutex
	ext     *conn
	cdpConn *conn
	tabInfo tabInfo
	swlogs  []string
}

type conn struct {
	nc net.Conn
	r  io.Reader
	w  *lockedWriter
	wm sync.Mutex
}

type lockedWriter struct {
	nc net.Conn
	mu *sync.Mutex
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.nc.Write(p)
}

func NewRelay() (*Relay, error) {
	tok, err := genToken()
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	r := &Relay{token: tok, ln: ln}
	mux := http.NewServeMux()
	mux.HandleFunc("/ext", r.handleExt)
	mux.HandleFunc("/cdp", r.handleCDP)
	mux.HandleFunc("/swlog", r.handleSWLog)

	go func() { _ = http.Serve(ln, mux) }()
	return r, nil
}

func (r *Relay) SWLogs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.swlogs...)
}

func (r *Relay) handleSWLog(w http.ResponseWriter, req *http.Request) {
	if req.URL.Query().Get("token") != r.token {
		http.Error(w, "bad token", http.StatusUnauthorized)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(req.Body, 1<<16))
	r.mu.Lock()
	r.swlogs = append(r.swlogs, string(body))
	r.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (r *Relay) Addr() string { return r.ln.Addr().String() }

func (r *Relay) Token() string { return r.token }

func (r *Relay) Close() error { return r.ln.Close() }

func (r *Relay) CDPURL() string { return "ws://" + r.ln.Addr().String() + "/cdp" }

func (r *Relay) Attached() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ext != nil
}

func genToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (r *Relay) handleExt(w http.ResponseWriter, req *http.Request) {
	if req.URL.Query().Get("token") != r.token {
		http.Error(w, "bad token", http.StatusUnauthorized)
		return
	}
	c, err := upgrade(w, req)
	if err != nil {
		return
	}
	r.mu.Lock()
	if r.ext != nil {
		r.ext.close()
	}
	r.ext = c
	r.mu.Unlock()
	r.serveExt(c)
}

func (r *Relay) handleCDP(w http.ResponseWriter, req *http.Request) {
	c, err := upgrade(w, req)
	if err != nil {
		return
	}
	r.serveCDP(c)
}

func upgrade(w http.ResponseWriter, req *http.Request) (*conn, error) {

	if !strings.EqualFold(req.Header.Get("Upgrade"), "websocket") {
		http.Error(w, "not a websocket upgrade", http.StatusBadRequest)
		return nil, errors.New("not a websocket upgrade")
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "no hijack", http.StatusInternalServerError)
		return nil, errors.New("response writer cannot hijack")
	}
	nc, rw, err := hj.Hijack()
	if err != nil {
		return nil, err
	}
	key := req.Header.Get("Sec-WebSocket-Key")
	accept := wsAccept(key)
	if _, err := fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", accept); err != nil {
		_ = nc.Close()
		return nil, err
	}
	if err := rw.Flush(); err != nil {
		_ = nc.Close()
		return nil, err
	}
	c := &conn{nc: nc, r: rw.Reader}
	c.w = &lockedWriter{nc: nc, mu: &c.wm}
	return c, nil
}

func wsAccept(key string) string {
	const magic = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	sum := sha1.Sum([]byte(key + magic))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func (c *conn) close() { _ = c.nc.Close() }

type wsRW struct {
	c *conn
}

func (s wsRW) Read(p []byte) (int, error)  { return s.c.r.Read(p) }
func (s wsRW) Write(p []byte) (int, error) { return s.c.w.Write(p) }

func (c *conn) writeText(b []byte) error {
	return wsutil.WriteServerMessage(wsRW{c}, ws.OpText, b)
}

func (c *conn) readText() ([]byte, error) {
	return wsutil.ReadClientText(wsRW{c})
}

func (r *Relay) serveExt(c *conn) {
	defer func() {
		r.mu.Lock()
		if r.ext == c {
			r.ext = nil
		}
		r.mu.Unlock()
		c.close()
	}()
	for {
		msg, err := c.readText()
		if err != nil {
			return
		}

		if isControl(msg) {
			r.handleControl(msg)
			continue
		}
		r.mu.Lock()
		cdp := r.cdpLocked()
		r.mu.Unlock()
		if cdp != nil {
			_ = cdp.writeText(msg)
		}
	}
}

func (r *Relay) serveCDP(c *conn) {
	r.mu.Lock()
	if old := r.cdpConn; old != nil && old != c {
		old.close()
	}
	r.setCDPLocked(c)
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.setCDPLocked(nil)
		r.mu.Unlock()
		c.close()
	}()
	for {
		msg, err := c.readText()
		if err != nil {
			return
		}
		if r.handleSynth(c, msg) {
			continue
		}
		r.mu.Lock()
		ext := r.ext
		r.mu.Unlock()
		if ext == nil {
			r.replyErr(c, msg, "no browser tab attached — click the k-brain extension icon on a tab")
			continue
		}
		_ = ext.writeText(msg)
	}
}

func (r *Relay) cdpLocked() *conn { return r.cdpConn }

func (r *Relay) setCDPLocked(c *conn) { r.cdpConn = c }

func (r *Relay) handleSynth(c *conn, msg []byte) bool {
	var f frame
	if err := json.Unmarshal(msg, &f); err != nil || f.ID == 0 || f.Method == "" {
		return false
	}
	switch f.Method {
	case "Target.setDiscoverTargets":

		r.reply(c, f.ID, `{}`)
		return true
	case "Target.getTargets":
		r.reply(c, f.ID, r.targetsJSON())
		return true
	case "Target.getTargetInfo":

		r.reply(c, f.ID, r.targetInfoJSON())
		return true
	case "Target.attachToTarget":

		r.reply(c, f.ID, `{"sessionId":"k-brain-ext"}`)
		return true
	case "Target.createTarget", "Browser.close", "Browser.getVersion":

		switch f.Method {
		case "Target.createTarget":
			r.replyErr(c, msg, "extension mode drives the pinned tab only; it cannot open new tabs")
		case "Browser.close":
			r.reply(c, f.ID, `{}`)
		default:
			r.reply(c, f.ID, `{"product":"k-brain-extension-relay"}`)
		}
		return true
	}
	return false
}

func (r *Relay) targetsJSON() string {
	r.mu.Lock()
	ti := r.tabInfo
	r.mu.Unlock()
	tid, title, url := ti.ID, ti.Title, ti.URL
	if tid == "" {
		tid = "k-brain-ext-tab"
	}
	b, _ := json.Marshal(map[string]any{
		"targetInfos": []map[string]any{{
			"targetId": tid,
			"type":     "page",
			"title":    title,
			"url":      url,
			"attached": true,
		}},
	})
	return string(b)
}

func (r *Relay) targetInfoJSON() string {
	r.mu.Lock()
	ti := r.tabInfo
	r.mu.Unlock()
	tid, title, url := ti.ID, ti.Title, ti.URL
	if tid == "" {
		tid = "k-brain-ext-tab"
	}
	b, _ := json.Marshal(map[string]any{
		"targetInfo": map[string]any{
			"targetId": tid,
			"type":     "page",
			"title":    title,
			"url":      url,
			"attached": true,
		},
	})
	return string(b)
}

func (r *Relay) reply(c *conn, id int64, result string) {
	_ = c.writeText([]byte(fmt.Sprintf(`{"id":%d,"result":%s}`, id, result)))
}

func (r *Relay) replyErr(c *conn, msg []byte, text string) {
	var f frame
	_ = json.Unmarshal(msg, &f)
	e, _ := json.Marshal(map[string]any{"code": -32000, "message": text})
	_ = c.writeText([]byte(fmt.Sprintf(`{"id":%d,"error":%s}`, f.ID, e)))
}

type tabInfo struct{ ID, Title, URL string }

func (r *Relay) handleControl(msg []byte) {
	var f frame
	if json.Unmarshal(msg, &f) != nil || !strings.HasPrefix(f.Method, "k-brain.") {
		return
	}
	if f.Method == "k-brain.attached" {
		var p struct {
			TabID int    `json:"tabId"`
			Title string `json:"title"`
			URL   string `json:"url"`
		}
		_ = json.Unmarshal(f.Params, &p)
		r.mu.Lock()
		r.tabInfo = tabInfo{ID: fmt.Sprintf("tab-%d", p.TabID), Title: p.Title, URL: p.URL}
		r.mu.Unlock()
	}
}

func isControl(msg []byte) bool {
	var f frame
	return json.Unmarshal(msg, &f) == nil && strings.HasPrefix(f.Method, "k-brain.")
}

func (r *Relay) WaitAttached(ctx context.Context) error {
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	for {
		if r.Attached() {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("no tab attached: click the k-brain extension icon on the tab to drive")
		case <-t.C:
		}
	}
}
