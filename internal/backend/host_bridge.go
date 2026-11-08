package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The host bridge lets the LiveAgent desktop app run the side effects of tools
// whose state lives in its own process (its terminal tabs, its Gateway tunnels).
// The desktop registers a loopback URL and a bearer token with PUT
// /v1/host-bridge; K-brain then calls POST {url}/v1/host/{command}. The
// registration lives only in this process's memory: it is never persisted, logged,
// published in events or history, or passed to child processes.

// hostBridgeCallTimeout bounds each host command; tests shorten it.
var hostBridgeCallTimeout = 10 * time.Second

const (
	hostBridgeMaxResponse   = 8 << 20
	hostBridgeMaxRequest    = 64 << 10
	hostBridgeMaxToken      = 4096
	hostBridgeNotConnected  = "LiveAgent desktop is not connected"
	hostToolReadTerminal    = "ReadTerminal"
	hostToolTunnelManager   = "TunnelManager"
	hostBridgeDefaultHostID = "liveagent-desktop"
)

// hostBridgeTools are the tools a host bridge may provide, in registration order.
var hostBridgeTools = []string{hostToolReadTerminal, hostToolTunnelManager}

var errHostBridgeNotConnected = errors.New(hostBridgeNotConnected)

func hostBridgeTool(name string) bool { return slices.Contains(hostBridgeTools, name) }

// hostBridge holds the current registration. Its zero value is disconnected.
type hostBridge struct {
	mu   sync.Mutex
	conn *hostBridgeConn
}

type hostBridgeConn struct {
	base   string
	token  string
	host   string
	tools  []string
	client *http.Client
}

type hostBridgeRegistration struct {
	URL   string   `json:"url"`
	Token string   `json:"token"`
	Host  string   `json:"host"`
	Tools []string `json:"tools"`
}

// hostBridgeStatus is what /v1/health and the registration endpoint report. It
// never carries the URL or the token.
type hostBridgeStatus struct {
	Connected bool     `json:"connected"`
	Tools     []string `json:"tools"`
}

func (b *hostBridge) current() *hostBridgeConn {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.conn
}

func (b *hostBridge) status() hostBridgeStatus {
	conn := b.current()
	if conn == nil {
		return hostBridgeStatus{Tools: []string{}}
	}
	return hostBridgeStatus{Connected: true, Tools: append([]string{}, conn.tools...)}
}

// provides reports whether a connected bridge advertises tool.
func (b *hostBridge) provides(tool string) bool {
	conn := b.current()
	return conn != nil && slices.Contains(conn.tools, tool)
}

func (b *hostBridge) set(conn *hostBridgeConn) {
	b.mu.Lock()
	old := b.conn
	b.conn = conn
	b.mu.Unlock()
	if old != nil {
		old.client.CloseIdleConnections()
	}
}

// validateHostBridgeURL accepts only plain http to a loopback host: 127.0.0.1,
// ::1 or localhost, with an optional port and no path, credentials or query.
func validateHostBridgeURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Opaque != "" {
		return "", errors.New("url must be http://127.0.0.1:<port>, http://[::1]:<port> or http://localhost:<port>")
	}
	if u.Scheme != "http" {
		return "", errors.New("url must use http")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", errors.New("url must not carry credentials, a path, a query or a fragment")
	}
	switch strings.ToLower(u.Hostname()) {
	case "127.0.0.1", "::1", "localhost":
	default:
		return "", errors.New("url must point to a loopback address (127.0.0.1, ::1 or localhost)")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", errors.New("url port is invalid")
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return "", errors.New("url port is invalid")
	}
	return "http://" + u.Host, nil
}

func validHostBridgeToken(token string) bool {
	if token == "" || len(token) > hostBridgeMaxToken {
		return false
	}
	for _, r := range token {
		if r <= ' ' || r == 0x7f {
			return false
		}
	}
	return true
}

// newHostBridgeClient talks only to loopback: it ignores proxy settings, never
// follows redirects, and drops any connection whose peer is not a loopback address
// (a hosts file can point localhost elsewhere).
func newHostBridgeClient() *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			conn, err := dialer.DialContext(ctx, network, address)
			if err != nil {
				return nil, err
			}
			if tcp, ok := conn.RemoteAddr().(*net.TCPAddr); !ok || !tcp.IP.IsLoopback() {
				_ = conn.Close()
				return nil, errors.New("host bridge address is not a loopback address")
			}
			return conn, nil
		},
		MaxIdleConns:    4,
		IdleConnTimeout: 30 * time.Second,
	}
	return &http.Client{
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func (s *Server) handleHostBridge(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPut:
		var in hostBridgeRegistration
		r.Body = http.MaxBytesReader(w, r.Body, hostBridgeMaxRequest)
		// Unknown fields are ignored so a newer desktop can add some.
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		base, err := validateHostBridgeURL(in.URL)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if !validHostBridgeToken(in.Token) {
			writeJSONError(w, http.StatusBadRequest, "token is required and must be printable without spaces")
			return
		}
		host := strings.TrimSpace(in.Host)
		if host == "" {
			host = hostBridgeDefaultHostID
		}
		if len(host) > 128 {
			writeJSONError(w, http.StatusBadRequest, "host is too long")
			return
		}
		// Tools K-brain does not know are ignored: the desktop may offer more than
		// this backend can register.
		var advertised []string
		for _, name := range hostBridgeTools {
			if slices.Contains(in.Tools, name) {
				advertised = append(advertised, name)
			}
		}
		s.hostBridge.set(&hostBridgeConn{base: base, token: in.Token, host: host, tools: advertised, client: newHostBridgeClient()})
		writeJSON(w, http.StatusOK, s.hostBridge.status())
	case http.MethodDelete:
		s.hostBridge.set(nil)
		writeJSON(w, http.StatusOK, s.hostBridge.status())
	default:
		w.Header().Set("Allow", "PUT, DELETE")
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// hostBridgeError is an error the desktop reported for a command.
type hostBridgeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *hostBridgeError) Error() string {
	message := strings.TrimSpace(e.Message)
	if message == "" {
		message = "LiveAgent desktop reported an error"
	}
	if code := strings.TrimSpace(e.Code); code != "" {
		return message + " (" + code + ")"
	}
	return message
}

// call runs one host command for tool and decodes its result into out. It fails
// at once when no bridge advertising tool is connected, after hostBridgeCallTimeout
// when the desktop does not answer, and when ctx (the run) is cancelled. Errors
// never include the bridge URL or token.
func (b *hostBridge) call(ctx context.Context, tool, command string, args any, out any) error {
	conn := b.current()
	if conn == nil || !slices.Contains(conn.tools, tool) {
		return errHostBridgeNotConnected
	}
	if args == nil {
		args = struct{}{}
	}
	body, err := json.Marshal(args)
	if err != nil {
		return err
	}
	callCtx, cancel := context.WithTimeout(ctx, hostBridgeCallTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, conn.base+"/v1/host/"+command, bytes.NewReader(body))
	if err != nil {
		return errors.New("LiveAgent desktop host bridge request could not be built")
	}
	req.Header.Set("Authorization", "Bearer "+conn.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := conn.client.Do(req)
	if err != nil {
		return hostBridgeTransportError(ctx, callCtx, command, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return fmt.Errorf("LiveAgent desktop host bridge answered %s with HTTP %d", command, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, hostBridgeMaxResponse+1))
	if err != nil {
		return hostBridgeTransportError(ctx, callCtx, command, err)
	}
	if len(data) > hostBridgeMaxResponse {
		return fmt.Errorf("LiveAgent desktop host bridge answer to %s exceeds %d MiB", command, hostBridgeMaxResponse>>20)
	}
	var envelope struct {
		OK     bool             `json:"ok"`
		Result json.RawMessage  `json:"result"`
		Error  *hostBridgeError `json:"error"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return fmt.Errorf("LiveAgent desktop host bridge answered %s with invalid JSON", command)
	}
	if !envelope.OK {
		if envelope.Error == nil {
			envelope.Error = &hostBridgeError{}
		}
		return envelope.Error
	}
	if out == nil || len(envelope.Result) == 0 || string(envelope.Result) == "null" {
		return nil
	}
	if err := json.Unmarshal(envelope.Result, out); err != nil {
		return fmt.Errorf("LiveAgent desktop host bridge result for %s is malformed: %v", command, err)
	}
	return nil
}

// hostBridgeTransportError describes a failed request without the URL that
// net/http errors carry.
func hostBridgeTransportError(ctx, callCtx context.Context, command string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if errors.Is(callCtx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("LiveAgent desktop did not answer %s within %s", command, hostBridgeCallTimeout)
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		if opErr.Op == "dial" {
			return fmt.Errorf("%s (host bridge unreachable)", hostBridgeNotConnected)
		}
		// Keep the cause, not the addresses OpError prints.
		err = opErr.Err
	}
	return fmt.Errorf("LiveAgent desktop host bridge request %s failed: %v", command, err)
}
