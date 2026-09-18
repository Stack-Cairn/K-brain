package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message) }

type diagHandler func(uri string, version int, diags []Diagnostic)

type client struct {
	stdin  io.Writer
	out    chan []byte
	pend   sync.Map
	nextID atomic.Int64
	dead   chan struct{}
	onDiag diagHandler
}

type rpcResponse struct {
	result json.RawMessage
	err    error
}

func newClient(stdin io.Writer, stdout io.Reader, onDiag diagHandler) *client {
	c := &client{
		stdin:  stdin,
		out:    make(chan []byte, 64),
		dead:   make(chan struct{}),
		onDiag: onDiag,
	}
	go c.readLoop(stdout)
	go c.writeLoop()
	return c
}

func (c *client) shutdown() {
	select {
	case <-c.dead:
	default:
		close(c.dead)
	}
}

func (c *client) isDead() bool {
	select {
	case <-c.dead:
		return true
	default:
		return false
	}
}

func (c *client) writeLoop() {
	for {
		select {
		case frame := <-c.out:
			if _, err := c.stdin.Write(frame); err != nil {
				c.shutdown()
				return
			}
		case <-c.dead:
			return
		}
	}
}

func (c *client) readLoop(r io.Reader) {
	defer c.shutdown()
	br := bufio.NewReader(r)
	for {
		body, err := readFrame(br)
		if err != nil {
			return
		}
		var msg rpcMessage
		if err := json.Unmarshal(body, &msg); err != nil {
			continue
		}
		switch {
		case msg.Method == "textDocument/publishDiagnostics":
			var p struct {
				URI         string `json:"uri"`
				Version     int    `json:"version"`
				Diagnostics []struct {
					Range struct {
						Start struct {
							Line      int `json:"line"`
							Character int `json:"character"`
						} `json:"start"`
					} `json:"range"`
					Severity int    `json:"severity"`
					Message  string `json:"message"`
				} `json:"diagnostics"`
			}
			if err := json.Unmarshal(msg.Params, &p); err == nil && c.onDiag != nil {
				diags := make([]Diagnostic, len(p.Diagnostics))
				for i, d := range p.Diagnostics {
					diags[i] = Diagnostic{
						Line:     d.Range.Start.Line + 1,
						Col:      d.Range.Start.Character + 1,
						Severity: d.Severity,
						Message:  d.Message,
					}
				}
				c.onDiag(p.URI, p.Version, diags)
			}
		case len(msg.ID) > 0 && msg.Method != "":

			c.send(rpcMessage{JSONRPC: "2.0", ID: msg.ID, Result: json.RawMessage("null")})
		case len(msg.ID) > 0:
			key := string(msg.ID)
			if ch, ok := c.pend.LoadAndDelete(key); ok {
				res := rpcResponse{result: msg.Result}
				if msg.Error != nil {
					res.err = msg.Error
				}
				ch.(chan rpcResponse) <- res
			}
		}
	}
}

func readFrame(br *bufio.Reader) ([]byte, error) {
	length := -1
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if v, ok := strings.CutPrefix(line, "Content-Length: "); ok {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil || n < 0 {
				return nil, fmt.Errorf("bad Content-Length %q", v)
			}
			length = n
		}
	}
	if length < 0 {
		return nil, errors.New("missing Content-Length header")
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(br, body); err != nil {
		return nil, err
	}
	return body, nil
}

const writeTimeout = 5 * time.Second

func (c *client) send(msg rpcMessage) {
	msg.JSONRPC = "2.0"
	body, err := json.Marshal(msg)
	if err != nil {
		return
	}
	frame := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))
	payload := append([]byte(frame), body...)
	select {
	case c.out <- payload:
	case <-c.dead:
	case <-time.After(writeTimeout):
		c.shutdown()
	}
}

func (c *client) request(ctx context.Context, method string, params, result any) error {
	n := c.nextID.Add(1)
	id := strconv.FormatInt(n, 10)
	ch := make(chan rpcResponse, 1)
	c.pend.Store(id, ch)
	defer c.pend.Delete(id)

	idRaw, _ := json.Marshal(n)
	paramsRaw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	select {
	case <-c.dead:
		return errors.New("lsp server connection closed")
	default:
	}
	c.send(rpcMessage{ID: idRaw, Method: method, Params: paramsRaw})

	select {
	case res := <-ch:
		if res.err != nil {
			return res.err
		}
		if result != nil && len(res.result) > 0 {
			return json.Unmarshal(res.result, result)
		}
		return nil
	case <-c.dead:
		return errors.New("lsp server connection closed")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *client) notify(method string, params any) {
	paramsRaw, err := json.Marshal(params)
	if err != nil {
		return
	}
	c.send(rpcMessage{Method: method, Params: paramsRaw})
}
