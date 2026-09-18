package computer

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

const ProtocolVersion = "k-brain-computer/1"

const tokenEnvVar = "K_BRAIN_COMPUTER_TOKEN"

const (
	errCodeUnknownApp      = 1
	errCodeNoAXPermission  = 2
	errCodeNoScreenPerm    = 3
	errCodeStaleGeneration = 4
	errCodeIndexOutOfRange = 5
	errCodeNotActionable   = 6
	errCodeScreenLocked    = 7
	errCodeBadToken        = 8
)

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return e.Message }

type StaleError struct{ Msg string }

func (e *StaleError) Error() string { return e.Msg }

type rpcRequest struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      int64          `json:"id"`
	Method  string         `json:"method"`
	Params  map[string]any `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

type AXElement struct {
	Index    int       `json:"index"`
	Role     string    `json:"role"`
	Subrole  string    `json:"subrole,omitempty"`
	Title    string    `json:"title,omitempty"`
	Value    string    `json:"value,omitempty"`
	Desc     string    `json:"desc,omitempty"`
	RoleDesc string    `json:"roleDescription,omitempty"`
	Position []float64 `json:"position,omitempty"`
	Size     []float64 `json:"size,omitempty"`
	Actions  []string  `json:"actions,omitempty"`
	Focused  bool      `json:"focused"`
	Enabled  bool      `json:"enabled"`
}

type Screenshot struct {
	JPEGBase64 string `json:"jpegBase64,omitempty"`
	Bytes      int    `json:"bytes,omitempty"`
	Err        string `json:"error,omitempty"`
}

type AppState struct {
	Generation int         `json:"generation"`
	App        string      `json:"app"`
	Elements   []AXElement `json:"elements"`
	Screenshot *Screenshot `json:"screenshot,omitempty"`
}

type RunningApp struct {
	Name     string `json:"name"`
	BundleID string `json:"bundleId"`
	PID      int    `json:"pid"`
	Active   bool   `json:"active"`
}

type TCCStatus struct {
	Accessibility   bool   `json:"accessibility"`
	ScreenRecording bool   `json:"screenRecording"`
	Pending         bool   `json:"pending,omitempty"`
	Hint            string `json:"hint,omitempty"`
}

type Helper struct {
	mu     sync.Mutex
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	reader *bufio.Reader
	token  string
	nextID int64
}

var shared = struct {
	mu  sync.Mutex
	h   *Helper
	err error
}{}

func Shared() (*Helper, error) {
	shared.mu.Lock()
	defer shared.mu.Unlock()
	if shared.h != nil {
		return shared.h, nil
	}
	if shared.err != nil {
		return nil, shared.err
	}
	h := &Helper{}
	if err := h.spawn(); err != nil {
		shared.err = err
		return nil, err
	}
	shared.h = h
	return h, nil
}

func ResetShared() {
	shared.mu.Lock()
	defer shared.mu.Unlock()
	if shared.h != nil {
		shared.h.kill()
	}
	shared.h, shared.err = nil, nil
}

func helperPath() (string, error) {
	if p := os.Getenv("K_BRAIN_COMPUTER_BIN"); p != "" {
		return p, nil
	}
	return ensureHelperBinary()
}

func (h *Helper) spawn() error {
	path, err := helperPath()
	if err != nil {
		return err
	}
	tok := make([]byte, 16)
	if _, err := rand.Read(tok); err != nil {
		return err
	}
	h.token = hex.EncodeToString(tok)

	h.cmd = exec.CommandContext(context.Background(), path)
	h.cmd.Env = append(os.Environ(), tokenEnvVar+"="+h.token)
	stdin, err := h.cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := h.cmd.StdoutPipe()
	if err != nil {
		return err
	}
	h.stdin = stdin
	h.reader = bufio.NewReaderSize(stdout, 4<<20)
	if err := h.cmd.Start(); err != nil {
		return fmt.Errorf("spawn k-brain-computer: %w", err)
	}

	announce, err := h.readLineTimeout(10 * time.Second)
	if err != nil {
		h.kill()
		return fmt.Errorf("k-brain-computer did not announce: %w", err)
	}
	if announce != ProtocolVersion {
		h.kill()
		return fmt.Errorf("k-brain-computer protocol mismatch: got %q, want %q (rebuild the driver: task driver)", announce, ProtocolVersion)
	}

	var hs struct {
		Version string `json:"version"`
	}
	if err := h.callLocked(context.Background(), "handshake", map[string]any{"token": h.token}, &hs); err != nil {
		h.kill()
		return fmt.Errorf("k-brain-computer handshake: %w", err)
	}
	if hs.Version != ProtocolVersion {
		h.kill()
		return fmt.Errorf("k-brain-computer handshake version mismatch: %q", hs.Version)
	}
	return nil
}

func (h *Helper) readLineTimeout(d time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	return h.readLineContext(ctx)
}

func (h *Helper) readLineContext(ctx context.Context) (string, error) {
	reader := h.reader
	type res struct {
		s   string
		err error
	}
	ch := make(chan res, 1)
	go func() {
		s, err := reader.ReadString('\n')
		ch <- res{s, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			return "", r.err
		}

		for len(r.s) > 0 && (r.s[len(r.s)-1] == '\n' || r.s[len(r.s)-1] == '\r') {
			r.s = r.s[:len(r.s)-1]
		}
		return r.s, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (h *Helper) kill() {
	if h.cmd != nil && h.cmd.Process != nil {
		_ = h.cmd.Process.Kill()
		_, _ = h.cmd.Process.Wait()
	}
	h.cmd = nil
}

func (h *Helper) restartLocked() error {
	h.kill()
	return h.spawn()
}

func (h *Helper) Call(ctx context.Context, method string, params map[string]any, out any) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if h.cmd == nil {
		if err := h.spawn(); err != nil {
			return err
		}
	}
	if params == nil {
		params = map[string]any{}
	}
	params["token"] = h.token
	err := h.callLocked(ctx, method, params, out)
	if err == nil {
		return nil
	}
	if rpcErr, ok := errors.AsType[*rpcError](err); ok {
		if rpcErr.Code == errCodeStaleGeneration {
			return &StaleError{Msg: rpcErr.Message}
		}
		return rpcErr
	}

	if ctx.Err() != nil {
		h.kill()
		return ctx.Err()
	}
	switch method {
	case "click", "type", "press", "scroll", "set", "select", "menu":
		h.kill()
		return fmt.Errorf("computer action outcome is unknown; read state before retrying: %w", err)
	}
	if rerr := h.restartLocked(); rerr != nil {
		return fmt.Errorf("k-brain-computer crashed and restart failed: %w (restart: %w)", err, rerr)
	}
	params["token"] = h.token
	err = h.callLocked(ctx, method, params, out)
	var rpcErr2 *rpcError
	if errors.As(err, &rpcErr2) && rpcErr2.Code == errCodeStaleGeneration {
		return &StaleError{Msg: rpcErr2.Message}
	}
	return err
}

func (h *Helper) callLocked(ctx context.Context, method string, params map[string]any, out any) error {
	if h.cmd == nil {
		return errors.New("k-brain-computer not running")
	}
	h.nextID++
	req := rpcRequest{JSONRPC: "2.0", ID: h.nextID, Method: method, Params: params}
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if _, err := h.stdin.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write to k-brain-computer: %w", err)
	}

	timeout := 30 * time.Second
	if method == "permissions.request" {
		timeout = 150 * time.Second
	}
	if dl, ok := ctx.Deadline(); ok && time.Until(dl) < timeout {
		timeout = time.Until(dl)
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	line, err := h.readLineContext(callCtx)
	if err != nil {
		return fmt.Errorf("read from k-brain-computer: %w", err)
	}
	var resp rpcResponse
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		return fmt.Errorf("bad frame from k-brain-computer: %q", line[:min(len(line), 120)])
	}
	if resp.JSONRPC != "2.0" || resp.ID != req.ID {
		return errors.New("mismatched computer RPC response")
	}
	if resp.Error != nil {
		return resp.Error
	}
	if out != nil && len(resp.Result) > 0 {
		return json.Unmarshal(resp.Result, out)
	}
	return nil
}

func (s *Screenshot) Decode() ([]byte, error) {
	if s == nil || s.JPEGBase64 == "" {
		return nil, nil
	}
	return base64.StdEncoding.DecodeString(s.JPEGBase64)
}
