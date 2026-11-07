package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/elicit"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// MCP form elicitation, client side.
//
// A server may ask the user for structured input while one of its tools
// runs: an elicitation/create request (protocol 2025-11-25) or an
// elicitation input request inside a tool result (multi-round-trip,
// 2026-07-28). Either way it reaches server.elicit, which
//
//   - accepts form mode only (-32602 for anything else);
//   - checks the requestedSchema against the subset in internal/elicit
//     (-32602 "invalid requestedSchema: ..." when it is outside);
//   - declines, without asking, an elicitation that arrives while none of the
//     server's tools is running, or past elicit.MaxPerCall in one call;
//   - otherwise asks the front end the tool call's context carries
//     (elicit.Handler), declining when there is none, and pauses the tool
//     timeout while the user answers;
//   - records the outcome in the call's history with sensitive values left
//     out, and logs only the server, tool, action and reason, never values.

var (
	errToolTimeout      = errors.New("mcp tool timed out")
	errElicitWithdrawn  = errors.New("the MCP server withdrew its elicitation")
	methodElicit        = "elicitation/create"
	methodElicitPattern = []byte(methodElicit)
)

// EnableElicitation makes the manager declare the MCP elicitation capability
// (form mode) and route servers' elicitations to the front end in the tool
// call's context (internal/elicit). Call it before Start; a manager without
// it declares no elicitation capability, so servers cannot ask.
func (m *Manager) EnableElicitation() {
	m.onChangeMu.Lock()
	m.elicitation = true
	m.onChangeMu.Unlock()
}

// activeCall is the tool call a server is running; calls are serialized per
// server (server.calling), so there is at most one.
type activeCall struct {
	ctx      context.Context
	tool     string
	deadline *callDeadline
	count    int
}

func invalidParams(format string, args ...any) error {
	return &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: fmt.Sprintf(format, args...)}
}

func (s *server) elicitHandler(tap *orderTap) func(context.Context, *sdkmcp.ElicitRequest) (*sdkmcp.ElicitResult, error) {
	return func(ctx context.Context, req *sdkmcp.ElicitRequest) (*sdkmcp.ElicitResult, error) {
		return s.elicit(ctx, req.Params, tap)
	}
}

func (s *server) elicit(reqCtx context.Context, p *sdkmcp.ElicitParams, tap *orderTap) (*sdkmcp.ElicitResult, error) {
	if p == nil {
		return nil, invalidParams("elicitation/create needs params")
	}
	if p.Mode != "" && p.Mode != "form" {
		return nil, invalidParams("unsupported elicitation mode %q: this client supports form mode only", p.Mode)
	}
	message := strings.TrimSpace(p.Message)
	if message == "" {
		return nil, invalidParams("elicitation message is required")
	}
	if utf8.RuneCountInString(message) > elicit.MaxMessageLen {
		return nil, invalidParams("elicitation message is longer than %d characters", elicit.MaxMessageLen)
	}
	schema, err := json.Marshal(p.RequestedSchema)
	if err != nil {
		return nil, invalidParams("invalid requestedSchema: %v", err)
	}
	fields, err := elicit.Parse(schema, tap.order(p.Message, schema))
	if err != nil {
		return nil, invalidParams("invalid requestedSchema: %v", err)
	}
	if string(schema) == "null" {
		schema = nil
	}

	s.mu.Lock()
	call := s.active
	n := 0
	if call != nil {
		call.count++
		n = call.count
	}
	s.mu.Unlock()
	decline := &sdkmcp.ElicitResult{Action: elicit.Decline}
	if call == nil {
		logf("%s: declined an elicitation that arrived while none of its tools was running", s.name)
		return decline, nil
	}
	rec := ai.Elicitation{Server: s.name, Message: message, Fields: fields}
	finish := func(action, reason string, content map[string]any) *sdkmcp.ElicitResult {
		rec.Action, rec.Reason = action, reason
		rec.Content, rec.Redacted = elicit.Redact(fields, content)
		tools.ReportElicitation(call.ctx, rec)
		logf("%s: elicitation during %s: %s (%s)", s.name, call.tool, action, reason)
		return &sdkmcp.ElicitResult{Action: action, Content: content}
	}
	if n > elicit.MaxPerCall {
		return finish(elicit.Decline, elicit.ReasonLimit, nil), nil
	}

	// One form at a time per server: multi-round-trip results may carry
	// several elicitations, which the SDK fulfills concurrently.
	s.elicitMu.Lock()
	defer s.elicitMu.Unlock()
	// Values (run identity, tool call id, history) come from the tool call;
	// the wait ends when the call is cancelled or the server withdraws the
	// request.
	ctx, cancel := context.WithCancelCause(call.ctx)
	defer cancel(nil)
	stop := context.AfterFunc(reqCtx, func() { cancel(errElicitWithdrawn) })
	defer stop()
	ask := elicit.Handler(ctx)
	if ask == nil {
		return finish(elicit.Decline, elicit.ReasonNoClient, nil), nil
	}
	if ctx.Err() != nil {
		return finish(elicit.Cancel, elicit.ReasonCancelled, nil), nil
	}
	call.deadline.pause()
	started := time.Now()
	resp := ask(ctx, elicit.Request{Server: s.name, Tool: call.tool, Message: message, Fields: fields, Schema: schema})
	tools.ReportUserWait(call.ctx, time.Since(started))
	call.deadline.resume()
	rec.ElicitationID = resp.ID
	reason := resp.Reason
	if reason == "" {
		reason = elicit.ReasonUser
	}
	switch resp.Action {
	case elicit.Accept:
		content, errs := elicit.Validate(fields, resp.Content)
		if errs != nil {
			// Front ends validate before accepting; this is a last guard so a
			// server never receives an answer outside its own schema.
			logf("%s: front end accepted an invalid elicitation answer; sending cancel (%d field errors)", s.name, len(errs))
			return finish(elicit.Cancel, elicit.ReasonCancelled, nil), nil
		}
		return finish(elicit.Accept, reason, content), nil
	case elicit.Decline:
		return finish(elicit.Decline, reason, nil), nil
	default:
		return finish(elicit.Cancel, reason, nil), nil
	}
}

// callDeadline is a tool call's timeout that stops counting while the user
// fills in an elicitation form.
type callDeadline struct {
	mu      sync.Mutex
	timer   *time.Timer
	left    time.Duration
	since   time.Time
	paused  int
	done    bool
	expired func()
}

func startDeadline(d time.Duration, expired func()) *callDeadline {
	dl := &callDeadline{left: d, since: time.Now(), expired: expired}
	dl.timer = time.AfterFunc(d, dl.fire)
	return dl
}

func (dl *callDeadline) fire() {
	dl.mu.Lock()
	if dl.done || dl.paused > 0 {
		dl.mu.Unlock()
		return
	}
	dl.done = true
	dl.mu.Unlock()
	dl.expired()
}

func (dl *callDeadline) pause() {
	dl.mu.Lock()
	defer dl.mu.Unlock()
	dl.paused++
	if dl.paused == 1 && !dl.done {
		if dl.timer.Stop() {
			dl.left -= time.Since(dl.since)
		} else {
			// The timer fired as the form opened; it expires on resume.
			dl.left = 0
		}
	}
}

func (dl *callDeadline) resume() {
	dl.mu.Lock()
	defer dl.mu.Unlock()
	dl.paused--
	if dl.paused == 0 && !dl.done {
		dl.since = time.Now()
		dl.timer = time.AfterFunc(max(dl.left, 0), dl.fire)
	}
}

func (dl *callDeadline) stop() {
	dl.mu.Lock()
	defer dl.mu.Unlock()
	dl.done = true
	dl.timer.Stop()
}

// orderTap remembers the property order of the requestedSchemas a server
// sends. The SDK decodes requestedSchema into a Go map, which loses the
// order the server wrote the fields in, and a form reads best in that order.
// The tap sees the raw messages first and keys each order by the message and
// the property names, which is how server.elicit finds it again.
type orderTap struct {
	mu   sync.Mutex
	seen map[string][]string
	keys []string
}

const tapKeep = 16

func newOrderTap() *orderTap { return &orderTap{seen: map[string][]string{}} }

func tapKey(message string, names []string) string {
	sorted := slices.Clone(names)
	slices.Sort(sorted)
	return message + "\x00" + strings.Join(sorted, "\x00")
}

// order returns the remembered order of schema's properties for message, or
// nil.
func (t *orderTap) order(message string, schema json.RawMessage) []string {
	if t == nil {
		return nil
	}
	var s struct {
		Properties json.RawMessage `json:"properties"`
	}
	if json.Unmarshal(schema, &s) != nil {
		return nil
	}
	names := objectKeys(s.Properties)
	if len(names) < 2 {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.seen[tapKey(message, names)]
}

// observeParams records the property order of an elicitation's params.
func (t *orderTap) observeParams(raw json.RawMessage) {
	var p struct {
		Message         string `json:"message"`
		RequestedSchema struct {
			Properties json.RawMessage `json:"properties"`
		} `json:"requestedSchema"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return
	}
	names := objectKeys(p.RequestedSchema.Properties)
	if len(names) < 2 {
		return
	}
	key := tapKey(p.Message, names)
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.seen[key]; !ok {
		t.keys = append(t.keys, key)
		if len(t.keys) > tapKeep {
			delete(t.seen, t.keys[0])
			t.keys = t.keys[1:]
		}
	}
	t.seen[key] = names
}

// observeResult records the elicitations inside a multi-round-trip result.
func (t *orderTap) observeResult(raw json.RawMessage) {
	if !bytes.Contains(raw, methodElicitPattern) {
		return
	}
	var r struct {
		InputRequests map[string]struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		} `json:"inputRequests"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return
	}
	for _, ir := range r.InputRequests {
		if ir.Method == methodElicit {
			t.observeParams(ir.Params)
		}
	}
}

// observeWire records the elicitations in one encoded JSON-RPC message.
func (t *orderTap) observeWire(data []byte) {
	if !bytes.Contains(data, methodElicitPattern) {
		return
	}
	var msg struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal(data, &msg) != nil {
		return
	}
	if msg.Method == methodElicit {
		t.observeParams(msg.Params)
	} else if len(msg.Result) > 0 {
		t.observeResult(msg.Result)
	}
}

// objectKeys returns the keys of a JSON object in the order they appear.
func objectKeys(raw json.RawMessage) []string {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil
		}
		key, ok := tok.(string)
		if !ok {
			return nil
		}
		var skip json.RawMessage
		if dec.Decode(&skip) != nil {
			return nil
		}
		keys = append(keys, key)
	}
	return keys
}

// wrap returns transport with the tap in front of its incoming messages.
func (t *orderTap) wrap(transport sdkmcp.Transport) sdkmcp.Transport {
	if st, ok := transport.(*sdkmcp.StreamableClientTransport); ok {
		// The streamable connection reports session changes through an
		// unexported interface a wrapper would hide, so tap its HTTP bodies
		// instead.
		client := http.DefaultClient
		if st.HTTPClient != nil {
			client = st.HTTPClient
		}
		tapped := *client
		base := tapped.Transport
		if base == nil {
			base = http.DefaultTransport
		}
		tapped.Transport = tapRoundTripper{base: base, tap: t}
		st.HTTPClient = &tapped
		return st
	}
	return tapTransport{inner: transport, tap: t}
}

type tapTransport struct {
	inner sdkmcp.Transport
	tap   *orderTap
}

func (t tapTransport) Connect(ctx context.Context) (sdkmcp.Connection, error) {
	conn, err := t.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return tapConn{Connection: conn, tap: t.tap}, nil
}

type tapConn struct {
	sdkmcp.Connection
	tap *orderTap
}

func (c tapConn) Read(ctx context.Context) (jsonrpc.Message, error) {
	msg, err := c.Connection.Read(ctx)
	switch m := msg.(type) {
	case *jsonrpc.Request:
		if m.Method == methodElicit {
			c.tap.observeParams(m.Params)
		}
	case *jsonrpc.Response:
		c.tap.observeResult(m.Result)
	}
	return msg, err
}

type tapRoundTripper struct {
	base http.RoundTripper
	tap  *orderTap
}

func (rt tapRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := rt.base.RoundTrip(req)
	if err != nil || resp.Body == nil {
		return resp, err
	}
	ct := resp.Header.Get("Content-Type")
	switch {
	case strings.HasPrefix(ct, "text/event-stream"):
		resp.Body = &tapBody{ReadCloser: resp.Body, tap: rt.tap, sse: true}
	case strings.HasPrefix(ct, "application/json"):
		resp.Body = &tapBody{ReadCloser: resp.Body, tap: rt.tap}
	}
	return resp, nil
}

// tapMaxBuffer bounds what a tapBody holds: an SSE line or a JSON body
// larger than this is not observed (its form falls back to the default
// field order).
const tapMaxBuffer = 4 << 20

// tapBody shows a response body to the tap as the SDK reads it. Each SSE
// data line is observed as soon as its newline is read, before the SDK can
// dispatch the event; a JSON body is observed at EOF.
type tapBody struct {
	io.ReadCloser
	tap  *orderTap
	sse  bool
	buf  []byte
	skip bool
}

func (b *tapBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.feed(p[:n])
	}
	if err == io.EOF && !b.sse && !b.skip {
		b.tap.observeWire(b.buf)
		b.buf = nil
	}
	return n, err
}

func (b *tapBody) feed(chunk []byte) {
	if !b.sse {
		if !b.skip && len(b.buf)+len(chunk) > tapMaxBuffer {
			b.skip, b.buf = true, nil
		}
		if !b.skip {
			b.buf = append(b.buf, chunk...)
		}
		return
	}
	for len(chunk) > 0 {
		i := bytes.IndexByte(chunk, '\n')
		if i < 0 {
			if !b.skip && len(b.buf)+len(chunk) > tapMaxBuffer {
				b.skip, b.buf = true, nil
			}
			if !b.skip {
				b.buf = append(b.buf, chunk...)
			}
			return
		}
		if !b.skip {
			line := append(b.buf, chunk[:i]...)
			if data, ok := bytes.CutPrefix(bytes.TrimRight(line, "\r"), []byte("data:")); ok {
				b.tap.observeWire(bytes.TrimSpace(data))
			}
		}
		b.buf, b.skip = b.buf[:0], false
		chunk = chunk[i+1:]
	}
}
