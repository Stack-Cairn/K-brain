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
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
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
	token      string
	ln         net.Listener
	mu         sync.Mutex
	ext        *conn
	cdpConn    *conn
	tabInfo    tabInfo
	swlogs     []string
	swlogBytes int
	server     *http.Server
	closed     bool
	workers    sync.WaitGroup
	closeOnce  sync.Once
	closeErr   error
	done       chan struct{}
	serveDone  chan struct{}
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
	r := &Relay{token: tok, ln: ln, done: make(chan struct{}), serveDone: make(chan struct{})}
	mux := http.NewServeMux()
	mux.HandleFunc("/ext", r.handler(r.handleExt))
	mux.HandleFunc("/cdp", r.handler(r.handleCDP))
	mux.HandleFunc("/swlog", r.handler(r.handleSWLog))
	r.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	go func() {
		defer close(r.serveDone)
		_ = r.server.Serve(ln)
	}()
	return r, nil
}

func (r *Relay) handler(fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		if r.closed {
			r.mu.Unlock()
			http.Error(w, "relay closed", http.StatusServiceUnavailable)
			return
		}
		r.workers.Add(1)
		r.mu.Unlock()
		defer r.workers.Done()
		fn(w, req)
	}
}

func (r *Relay) Addr() string { return r.ln.Addr().String() }

func (r *Relay) Token() string { return r.token }

func (r *Relay) Close() error {
	r.closeOnce.Do(func() {
		r.mu.Lock()
		r.closed = true
		close(r.done)
		ext, cdp := r.ext, r.cdpConn
		r.ext, r.cdpConn = nil, nil
		r.tabInfo = tabInfo{}
		r.mu.Unlock()
		if ext != nil {
			ext.close()
		}
		if cdp != nil {
			cdp.close()
		}
		r.closeErr = r.server.Close()
		<-r.serveDone
		r.workers.Wait()
	})
	return r.closeErr
}

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
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		http.Error(w, "relay closed", http.StatusServiceUnavailable)
		return
	}
	c, err := upgrade(w, req)
	if err != nil {
		r.mu.Unlock()
		return
	}
	if r.ext != nil {
		r.ext.close()
	}
	r.ext = c
	r.tabInfo = tabInfo{}
	r.mu.Unlock()
	r.serveExt(c)
}

func (r *Relay) handleCDP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		http.Error(w, "relay closed", http.StatusServiceUnavailable)
		return
	}
	c, err := upgrade(w, req)
	if err != nil {
		r.mu.Unlock()
		return
	}
	if old := r.cdpConn; old != nil {
		old.close()
	}
	r.cdpConn = c
	r.mu.Unlock()
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
	if err := nc.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		_ = nc.Close()
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
	_ = nc.SetWriteDeadline(time.Time{})
	return &conn{nc: nc, r: rw.Reader}, nil
}

func wsAccept(key string) string {
	const magic = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	sum := sha1.Sum([]byte(key + magic))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func (r *Relay) serveExt(c *conn) {
	defer func() {
		r.mu.Lock()
		if r.ext == c {
			r.ext = nil
			r.tabInfo = tabInfo{}
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
			r.handleControl(c, msg)
			continue
		}
		r.mu.Lock()
		if r.ext != c || r.closed {
			r.mu.Unlock()
			return
		}
		cdp := r.cdpLocked()
		r.mu.Unlock()
		if cdp != nil {
			_ = cdp.writeText(msg)
		}
	}
}

func (r *Relay) serveCDP(c *conn) {
	defer func() {
		r.mu.Lock()
		if r.cdpConn == c {
			r.cdpConn = nil
		}
		r.mu.Unlock()
		c.close()
	}()
	for {
		msg, err := c.readText()
		if err != nil {
			return
		}
		r.mu.Lock()
		current := !r.closed && r.cdpConn == c
		r.mu.Unlock()
		if !current {
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

func (r *Relay) handleControl(c *conn, msg []byte) {
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
		if r.ext == c && !r.closed {
			r.tabInfo = tabInfo{ID: fmt.Sprintf("tab-%d", p.TabID), Title: p.Title, URL: p.URL}
		}
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
		case <-r.done:
			return net.ErrClosed
		case <-ctx.Done():
			return fmt.Errorf("no tab attached: click the k-brain extension icon on the tab to drive: %w", ctx.Err())
		case <-t.C:
		}
	}
}
