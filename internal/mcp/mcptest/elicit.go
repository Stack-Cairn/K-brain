// Package mcptest provides a minimal MCP server for tests that need to
// control the exact bytes a server sends, such as an elicitation's
// requestedSchema with its property order or a schema outside the subset.
package mcptest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"
)

// ElicitServerEnv, when set in a test binary's environment, tells its
// TestMain to serve ServeElicit on stdin/stdout instead of running tests, so
// that a test can configure the binary itself as a stdio MCP server.
const ElicitServerEnv = "K_BRAIN_TEST_FAKE_MCP_ELICIT"

// ServeElicit serves a minimal MCP server (protocol 2025-11-25) over
// newline-delimited JSON-RPC on r and w until r ends. It refuses
// server/discover, so clients fall back to initialize. Tools:
//
//   - ask {message, schema, mode, times}: sends times (default 1)
//     elicitation/create requests with message, mode (omitted when empty)
//     and schema (raw JSON text, sent verbatim; omitted when empty), and
//     returns a JSON array with the client's reply to each: {"result": ...}
//     or {"error": ...}.
//   - caps: returns the capabilities the client sent in initialize.
//   - orphan: returns at once, then sends an elicitation/create outside any
//     tool call and passes the client's reply to orphans (see
//     ServeElicitOrphans).
func ServeElicit(r io.Reader, w io.Writer) error {
	return ServeElicitOrphans(r, w, nil)
}

// ServeElicitOrphans is ServeElicit, passing the client's replies to orphan
// elicitations to orphans.
func ServeElicitOrphans(r io.Reader, w io.Writer, orphans chan<- json.RawMessage) error {
	s := &elicitServer{w: w, pending: map[string]chan json.RawMessage{}, orphans: orphans}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for sc.Scan() {
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal(sc.Bytes(), &msg) != nil {
			continue
		}
		if msg.Method == "" {
			s.deliver(msg.ID, append(json.RawMessage(nil), sc.Bytes()...))
			continue
		}
		if len(msg.ID) == 0 {
			continue // notification
		}
		go s.handle(msg.ID, msg.Method, msg.Params)
	}
	return sc.Err()
}

type elicitServer struct {
	wmu     sync.Mutex
	w       io.Writer
	mu      sync.Mutex
	pending map[string]chan json.RawMessage
	next    int
	caps    json.RawMessage
	orphans chan<- json.RawMessage
}

func (s *elicitServer) write(v any) {
	data, _ := json.Marshal(v)
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, _ = s.w.Write(append(data, '\n'))
}

func (s *elicitServer) reply(id json.RawMessage, result any) {
	s.write(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (s *elicitServer) fail(id json.RawMessage, code int, message string) {
	s.write(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}})
}

func (s *elicitServer) deliver(id json.RawMessage, msg json.RawMessage) {
	var key string
	_ = json.Unmarshal(id, &key)
	s.mu.Lock()
	ch := s.pending[key]
	delete(s.pending, key)
	s.mu.Unlock()
	if ch != nil {
		ch <- msg
	}
}

// request sends a request to the client and returns its reply as
// {"result": ...} or {"error": ...}.
func (s *elicitServer) request(method string, params any) json.RawMessage {
	s.mu.Lock()
	s.next++
	id := fmt.Sprintf("srv-%d", s.next)
	ch := make(chan json.RawMessage, 1)
	s.pending[id] = ch
	s.mu.Unlock()
	s.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	select {
	case msg := <-ch:
		var reply struct {
			Result json.RawMessage `json:"result,omitempty"`
			Error  json.RawMessage `json:"error,omitempty"`
		}
		_ = json.Unmarshal(msg, &reply)
		out, _ := json.Marshal(reply)
		return out
	case <-time.After(2 * time.Minute):
		return json.RawMessage(`{"error":"no reply"}`)
	}
}

func (s *elicitServer) handle(id json.RawMessage, method string, params json.RawMessage) {
	switch method {
	case "initialize":
		var p struct {
			Capabilities json.RawMessage `json:"capabilities"`
		}
		_ = json.Unmarshal(params, &p)
		s.mu.Lock()
		s.caps = p.Capabilities
		s.mu.Unlock()
		s.reply(id, map[string]any{
			"protocolVersion": "2025-11-25",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "fake-elicit", "version": "1.0.0"},
		})
	case "ping":
		s.reply(id, map[string]any{})
	case "tools/list":
		object := map[string]any{"type": "object"}
		s.reply(id, map[string]any{"tools": []any{
			map[string]any{"name": "ask", "description": "Ask the user through elicitation.", "inputSchema": object},
			map[string]any{"name": "caps", "description": "Client capabilities.", "inputSchema": object},
			map[string]any{"name": "orphan", "description": "Elicit outside a tool call.", "inputSchema": object},
		}})
	case "tools/call":
		var p struct {
			Name      string `json:"name"`
			Arguments struct {
				Message string `json:"message"`
				Schema  string `json:"schema"`
				Mode    string `json:"mode"`
				Times   int    `json:"times"`
			} `json:"arguments"`
		}
		_ = json.Unmarshal(params, &p)
		args := p.Arguments
		params := map[string]any{"message": args.Message}
		if args.Mode != "" {
			params["mode"] = args.Mode
		}
		if args.Schema != "" {
			params["requestedSchema"] = json.RawMessage(args.Schema)
		}
		switch p.Name {
		case "ask":
			replies := []json.RawMessage{}
			for range max(args.Times, 1) {
				replies = append(replies, s.request("elicitation/create", params))
			}
			s.text(id, replies)
		case "caps":
			s.mu.Lock()
			caps := s.caps
			s.mu.Unlock()
			s.text(id, caps)
		case "orphan":
			s.text(id, "ok")
			go func() {
				// Sent once the tool call has ended on the client.
				time.Sleep(100 * time.Millisecond)
				reply := s.request("elicitation/create", map[string]any{"message": "orphan", "requestedSchema": map[string]any{"type": "object", "properties": map[string]any{}}})
				if s.orphans != nil {
					s.orphans <- reply
				}
			}()
		default:
			s.fail(id, -32602, "unknown tool "+p.Name)
		}
	default:
		s.fail(id, -32601, "method not found: "+method)
	}
}

func (s *elicitServer) text(id json.RawMessage, v any) {
	data, _ := json.Marshal(v)
	s.reply(id, map[string]any{"content": []any{map[string]any{"type": "text", "text": string(data)}}})
}
