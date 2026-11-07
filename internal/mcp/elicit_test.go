package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/elicit"
	"github.com/Stack-Cairn/K-brain/internal/mcp/mcptest"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func fakeElicitTransport(t *testing.T, orphans chan<- json.RawMessage) sdkmcp.Transport {
	t.Helper()
	clientR, serverW := io.Pipe()
	serverR, clientW := io.Pipe()
	go func() {
		_ = mcptest.ServeElicitOrphans(serverR, serverW, orphans)
		_ = serverW.Close()
	}()
	t.Cleanup(func() {
		_ = clientW.Close()
		_ = serverR.Close()
	})
	return &sdkmcp.IOTransport{Reader: clientR, Writer: clientW}
}

func newElicitManager(t *testing.T, enable bool, toolTimeout int) *Manager {
	return newElicitManagerOrphans(t, enable, toolTimeout, nil)
}

func newElicitManagerOrphans(t *testing.T, enable bool, toolTimeout int, orphans chan<- json.RawMessage) *Manager {
	t.Helper()
	m := NewManager(map[string]ServerConfig{"forms": {Command: []string{"fake"}, StartupTimeout: 5, ToolTimeout: toolTimeout}})
	m.connectTransport = func(context.Context, ServerConfig, *ringBuffer) (sdkmcp.Transport, error) {
		return fakeElicitTransport(t, orphans), nil
	}
	if enable {
		m.EnableElicitation()
	}
	t.Cleanup(m.Close)
	m.Start(context.Background())
	waitReady(t, m)
	return m
}

type elicitReply struct {
	Result *struct {
		Action  string         `json:"action"`
		Content map[string]any `json:"content"`
	} `json:"result"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// ask runs the fake server's ask tool and returns the client's replies as
// the server saw them, and the tool result.
func ask(t *testing.T, ctx context.Context, m *Manager, args map[string]any) ([]elicitReply, tools.Result) {
	t.Helper()
	raw, _ := json.Marshal(args)
	res := tools.ExecuteResult(ctx, m.Tools(), "mcp__forms__ask", raw, false)
	if res.Failed {
		t.Fatalf("ask failed: %s", res.Text)
	}
	var replies []elicitReply
	if err := json.Unmarshal([]byte(res.Text), &replies); err != nil {
		t.Fatalf("decode %q: %v", res.Text, err)
	}
	return replies, res
}

const loginSchema = `{"type":"object","properties":{"username":{"type":"string","title":"User"},"password":{"type":"string","title":"Password"},"remember":{"type":"boolean","default":false},"region":{"type":"string","oneOf":[{"const":"eu","title":"Europe"},{"const":"us","title":"Americas"}]}},"required":["username","password"]}`

func hook(fn elicit.Func) context.Context { return elicit.With(context.Background(), fn) }

func TestElicitCapabilityDeclared(t *testing.T) {
	for _, enable := range []bool{true, false} {
		m := newElicitManager(t, enable, 5)
		out := tools.Execute(context.Background(), m.Tools(), "mcp__forms__caps", json.RawMessage(`{}`))
		var caps map[string]json.RawMessage
		if err := json.Unmarshal([]byte(out), &caps); err != nil {
			t.Fatalf("caps %q: %v", out, err)
		}
		got, ok := caps["elicitation"]
		if ok != enable {
			t.Fatalf("enable=%v: capabilities = %s", enable, out)
		}
		if enable && string(got) != `{"form":{}}` {
			t.Fatalf("elicitation capability = %s, want form mode", got)
		}
	}
}

func TestElicitAcceptKeepsServerOrderAndRecordsHistory(t *testing.T) {
	m := newElicitManager(t, true, 5)
	var got elicit.Request
	ctx := hook(func(_ context.Context, req elicit.Request) elicit.Response {
		got = req
		return elicit.Response{Action: elicit.Accept, ID: "el-1", Content: map[string]any{"username": "ada", "password": "hunter2", "region": "eu"}}
	})
	replies, res := ask(t, ctx, m, map[string]any{"message": "Sign in to the tracker", "schema": loginSchema})
	r := replies[0]
	if r.Result == nil || r.Result.Action != "accept" {
		t.Fatalf("reply = %+v", r)
	}
	// The SDK fills defaults the user left out.
	want := map[string]any{"username": "ada", "password": "hunter2", "region": "eu", "remember": false}
	if !reflect.DeepEqual(r.Result.Content, want) {
		t.Fatalf("server received %v, want %v", r.Result.Content, want)
	}
	if got.Server != "forms" || got.Tool != "ask" || got.Message != "Sign in to the tracker" {
		t.Fatalf("request = %+v", got)
	}
	var names []string
	for _, f := range got.Fields {
		names = append(names, f.Name)
	}
	if !reflect.DeepEqual(names, []string{"username", "password", "remember", "region"}) {
		t.Fatalf("field order = %v, want the server's", names)
	}
	if !got.Fields[1].Sensitive || got.Fields[0].Sensitive {
		t.Fatalf("sensitive flags = %+v", got.Fields)
	}
	if !json.Valid(got.Schema) || !strings.Contains(string(got.Schema), `"oneOf"`) {
		t.Fatalf("schema = %s", got.Schema)
	}
	if len(res.Elicitations) != 1 {
		t.Fatalf("history = %+v", res.Elicitations)
	}
	h := res.Elicitations[0]
	if h.ElicitationID != "el-1" || h.Server != "forms" || h.Action != "accept" || h.Reason != "user" {
		t.Fatalf("history = %+v", h)
	}
	if _, ok := h.Content["password"]; ok || !reflect.DeepEqual(h.Redacted, []string{"password"}) || h.Content["username"] != "ada" {
		t.Fatalf("history content = %v redacted = %v; the password must be left out", h.Content, h.Redacted)
	}
}

func TestElicitDeclineAndCancel(t *testing.T) {
	m := newElicitManager(t, true, 5)
	for _, action := range []string{elicit.Decline, elicit.Cancel} {
		ctx := hook(func(context.Context, elicit.Request) elicit.Response {
			// Content with a decline or cancel is never sent.
			return elicit.Response{Action: action, Content: map[string]any{"username": "x"}}
		})
		replies, res := ask(t, ctx, m, map[string]any{"message": "Continue?", "schema": loginSchema})
		if r := replies[0]; r.Result == nil || r.Result.Action != action || r.Result.Content != nil {
			t.Fatalf("%s: reply = %+v", action, r.Result)
		}
		if h := res.Elicitations[0]; h.Action != action || h.Content != nil {
			t.Fatalf("%s: history = %+v", action, h)
		}
	}
}

func TestElicitRejectsOutsideSubset(t *testing.T) {
	m := newElicitManager(t, true, 5)
	asked := false
	ctx := hook(func(context.Context, elicit.Request) elicit.Response {
		asked = true
		return elicit.Response{Action: elicit.Accept}
	})
	// The SDK's own, more lenient check refuses some schemas first, with its
	// own messages; K-brain's subset check refuses the rest.
	cases := []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"message": "x", "schema": `{"type":"object","properties":{"a":{"type":"string","$ref":"#/b"}}}`}, `invalid requestedSchema: property "a": keyword "$ref" is outside`},
		{map[string]any{"message": "x", "schema": `{"type":"object","properties":{"a":{"type":"array","items":{"type":"string"}}}}`}, `items must specify enum`},
		{map[string]any{"message": "x", "schema": `{"type":"object","properties":{"a":{"type":"string","enumNames":["A"],"enum":["a"],"oneOf":[{"const":"a","title":"A"}]}}}`}, `use either enum or oneOf`},
		{map[string]any{"message": "x", "schema": `{"type":"object","properties":{"a":{"type":"integer","minimum":1.2,"maximum":1.8}}}`}, `invalid requestedSchema: property "a": no integer lies between`},
		{map[string]any{"message": "x", "schema": `{"type":"object","properties":{"a":{"type":"string","enum":["x","x"]}}}`}, `invalid requestedSchema: property "a": option "x" appears twice`},
		{map[string]any{"message": "x", "schema": `{"type":"object","properties":{"a":{"type":"string","format":"ipv4"}}}`}, `unsupported format`},
		{map[string]any{"message": "x", "schema": `{"type":"object","properties":{"a":{"type":"object"}}}`}, ``},
		{map[string]any{"message": "x", "mode": "url"}, ``},
		{map[string]any{"message": "", "schema": `{"type":"object"}`}, `message is required`},
	}
	for _, c := range cases {
		replies, res := ask(t, ctx, m, c.args)
		r := replies[0]
		if r.Error == nil || r.Error.Code != -32602 || !strings.Contains(r.Error.Message, c.want) {
			t.Errorf("%v: reply = %+v / %+v, want -32602 containing %q", c.args, r.Result, r.Error, c.want)
		}
		if len(res.Elicitations) != 0 {
			t.Errorf("%v: refused request recorded: %+v", c.args, res.Elicitations)
		}
	}
	if asked {
		t.Fatal("the user was asked about a schema outside the subset")
	}
}

func TestElicitWithoutFrontEndDeclines(t *testing.T) {
	m := newElicitManager(t, true, 5)
	old := elicit.Default
	elicit.Default = nil
	t.Cleanup(func() { elicit.Default = old })
	replies, res := ask(t, context.Background(), m, map[string]any{"message": "Pick", "schema": loginSchema})
	if r := replies[0]; r.Result == nil || r.Result.Action != "decline" {
		t.Fatalf("reply = %+v %+v", r.Result, r.Error)
	}
	if h := res.Elicitations[0]; h.Reason != elicit.ReasonNoClient || h.Action != "decline" {
		t.Fatalf("history = %+v", h)
	}
}

func TestElicitInvalidAnswerFromFrontEndIsCancelled(t *testing.T) {
	m := newElicitManager(t, true, 5)
	ctx := hook(func(context.Context, elicit.Request) elicit.Response {
		return elicit.Response{Action: elicit.Accept, Content: map[string]any{"username": "ada"}}
	})
	replies, res := ask(t, ctx, m, map[string]any{"message": "Sign in", "schema": loginSchema})
	if r := replies[0]; r.Result == nil || r.Result.Action != "cancel" {
		t.Fatalf("reply = %+v", r.Result)
	}
	if h := res.Elicitations[0]; h.Action != "cancel" {
		t.Fatalf("history = %+v", h)
	}
}

func TestElicitLimitPerCall(t *testing.T) {
	m := newElicitManager(t, true, 10)
	asked := 0
	ctx := hook(func(context.Context, elicit.Request) elicit.Response {
		asked++
		return elicit.Response{Action: elicit.Accept}
	})
	replies, res := ask(t, ctx, m, map[string]any{"message": "Again?", "schema": `{"type":"object","properties":{}}`, "times": elicit.MaxPerCall + 2})
	if asked != elicit.MaxPerCall {
		t.Fatalf("asked %d times, want %d", asked, elicit.MaxPerCall)
	}
	for i, r := range replies {
		want := "accept"
		if i >= elicit.MaxPerCall {
			want = "decline"
		}
		if r.Result == nil || r.Result.Action != want {
			t.Fatalf("reply %d = %+v, want %s", i, r.Result, want)
		}
	}
	if last := res.Elicitations[len(res.Elicitations)-1]; last.Reason != elicit.ReasonLimit {
		t.Fatalf("last history = %+v", last)
	}
}

func TestElicitOutsideToolCallDeclines(t *testing.T) {
	orphans := make(chan json.RawMessage, 1)
	m := newElicitManagerOrphans(t, true, 5, orphans)
	asked := make(chan struct{}, 1)
	old := elicit.Default
	elicit.Default = func(context.Context, elicit.Request) elicit.Response {
		asked <- struct{}{}
		return elicit.Response{Action: elicit.Accept}
	}
	t.Cleanup(func() { elicit.Default = old })
	if out := tools.Execute(context.Background(), m.Tools(), "mcp__forms__orphan", json.RawMessage(`{}`)); out != `"ok"` {
		t.Fatalf("orphan = %q", out)
	}
	var last json.RawMessage
	select {
	case last = <-orphans:
	case <-time.After(5 * time.Second):
		t.Fatal("no reply to the orphan elicitation")
	}
	if !strings.Contains(string(last), `"action":"decline"`) {
		t.Fatalf("orphan reply = %s", last)
	}
	select {
	case <-asked:
		t.Fatal("an elicitation outside a tool call reached the user")
	default:
	}
}

func TestElicitPausesToolTimeout(t *testing.T) {
	m := newElicitManager(t, true, 1)
	ctx := hook(func(context.Context, elicit.Request) elicit.Response {
		time.Sleep(1500 * time.Millisecond)
		return elicit.Response{Action: elicit.Decline}
	})
	replies, res := ask(t, ctx, m, map[string]any{"message": "Slow", "schema": `{"type":"object"}`})
	if replies[0].Result == nil || replies[0].Result.Action != "decline" {
		t.Fatalf("reply = %+v", replies[0])
	}
	if res.Duration >= time.Second {
		t.Fatalf("duration %s includes the time the user spent on the form", res.Duration)
	}
}

func TestElicitCallCancelledWhileWaiting(t *testing.T) {
	m := newElicitManager(t, true, 5)
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	var sawCancel bool
	ctx = elicit.With(ctx, func(waitCtx context.Context, _ elicit.Request) elicit.Response {
		close(started)
		<-waitCtx.Done()
		sawCancel = true
		return elicit.Response{Action: elicit.Cancel, Reason: elicit.ReasonCancelled}
	})
	done := make(chan tools.Result, 1)
	go func() {
		done <- tools.ExecuteResult(ctx, m.Tools(), "mcp__forms__ask", json.RawMessage(`{"message":"Wait","schema":"{\"type\":\"object\"}"}`), false)
	}()
	<-started
	cancel()
	select {
	case res := <-done:
		if !res.Cancelled || !sawCancel {
			t.Fatalf("result = %+v, sawCancel = %v", res, sawCancel)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("tool call did not end after cancellation")
	}
}

func TestElicitMultiRoundTrip(t *testing.T) {
	srv := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "mrtr", Version: "1"}, nil)
	var mu sync.Mutex
	var received *sdkmcp.ElicitResult
	sdkmcp.AddTool(srv, &sdkmcp.Tool{Name: "deploy"}, func(_ context.Context, req *sdkmcp.CallToolRequest, _ struct{}) (*sdkmcp.CallToolResult, any, error) {
		if len(req.Params.InputResponses) == 0 {
			return &sdkmcp.CallToolResult{
				InputRequests: sdkmcp.InputRequestMap{"confirm": &sdkmcp.ElicitParams{
					Message:         "Deploy to production?",
					RequestedSchema: json.RawMessage(`{"type":"object","properties":{"zone":{"type":"string","enum":["a","b"]},"approve":{"type":"boolean"}},"required":["approve"]}`),
				}},
				RequestState: "s1",
			}, nil, nil
		}
		mu.Lock()
		received, _ = req.Params.InputResponses["confirm"].(*sdkmcp.ElicitResult)
		mu.Unlock()
		return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "deployed"}}}, nil, nil
	})
	m := NewManager(map[string]ServerConfig{"ops": {Command: []string{"ops"}, StartupTimeout: 5, ToolTimeout: 5}})
	m.connectTransport = func(context.Context, ServerConfig, *ringBuffer) (sdkmcp.Transport, error) {
		clientT, serverT := sdkmcp.NewInMemoryTransports()
		ss, err := srv.Connect(context.Background(), serverT, nil)
		if err != nil {
			return nil, err
		}
		t.Cleanup(func() { _ = ss.Close() })
		return clientT, nil
	}
	m.EnableElicitation()
	t.Cleanup(m.Close)
	m.Start(context.Background())
	waitReady(t, m)

	var got elicit.Request
	ctx := hook(func(_ context.Context, req elicit.Request) elicit.Response {
		got = req
		return elicit.Response{Action: elicit.Accept, Content: map[string]any{"approve": true, "zone": "b"}}
	})
	res := tools.ExecuteResult(ctx, m.Tools(), "mcp__ops__deploy", json.RawMessage(`{}`), false)
	if res.Failed || res.Text != "deployed" {
		t.Fatalf("deploy = %+v", res)
	}
	mu.Lock()
	defer mu.Unlock()
	if received == nil || received.Action != "accept" || received.Content["zone"] != "b" || received.Content["approve"] != true {
		t.Fatalf("server received %+v", received)
	}
	var names []string
	for _, f := range got.Fields {
		names = append(names, f.Name)
	}
	if got.Server != "ops" || !reflect.DeepEqual(names, []string{"zone", "approve"}) {
		t.Fatalf("request = %+v (fields %v)", got, names)
	}
	if len(res.Elicitations) != 1 || res.Elicitations[0].Action != "accept" {
		t.Fatalf("history = %+v", res.Elicitations)
	}
}

func TestOrderTapStreamable(t *testing.T) {
	tap := newOrderTap()
	body := "event: message\r\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"elicitation/create\",\"params\":{\"message\":\"m\",\"requestedSchema\":{\"type\":\"object\",\"properties\":{\"z\":{\"type\":\"string\"},\"a\":{\"type\":\"string\"}}}}}\r\n\r\n"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// Split mid-line to exercise buffering.
		_, _ = io.WriteString(w, body[:40])
		w.(http.Flusher).Flush()
		_, _ = io.WriteString(w, body[40:])
	}))
	defer ts.Close()
	st := &sdkmcp.StreamableClientTransport{Endpoint: ts.URL}
	if tap.wrap(st) != st || st.HTTPClient == nil {
		t.Fatal("streamable transport not tapped in place")
	}
	resp, err := st.HTTPClient.Get(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if got := tap.order("m", json.RawMessage(`{"properties":{"a":{},"z":{}}}`)); !reflect.DeepEqual(got, []string{"z", "a"}) {
		t.Fatalf("order = %v", got)
	}

	// A JSON body (a multi-round-trip result) is read at EOF.
	result := `{"jsonrpc":"2.0","id":2,"result":{"inputRequests":{"x":{"method":"elicitation/create","params":{"message":"n","requestedSchema":{"properties":{"q":{},"b":{}}}}}}}}`
	b := &tapBody{ReadCloser: io.NopCloser(bytes.NewBufferString(result)), tap: tap}
	_, _ = io.Copy(io.Discard, b)
	if got := tap.order("n", json.RawMessage(`{"properties":{"b":{},"q":{}}}`)); !reflect.DeepEqual(got, []string{"q", "b"}) {
		t.Fatalf("order = %v", got)
	}
	for i := range tapKeep + 1 {
		tap.observeParams(json.RawMessage(`{"message":"k` + strings.Repeat("x", i) + `","requestedSchema":{"properties":{"b":{},"a":{}}}}`))
	}
	if tap.order("m", json.RawMessage(`{"properties":{"a":{},"z":{}}}`)) != nil || len(tap.seen) != tapKeep {
		t.Fatalf("tap kept %d orders, want the last %d", len(tap.seen), tapKeep)
	}
}

func TestCallDeadline(t *testing.T) {
	fired := make(chan struct{})
	dl := startDeadline(80*time.Millisecond, func() { close(fired) })
	time.Sleep(40 * time.Millisecond)
	dl.pause()
	time.Sleep(120 * time.Millisecond)
	select {
	case <-fired:
		t.Fatal("deadline fired while paused")
	default:
	}
	dl.resume()
	select {
	case <-fired:
	case <-time.After(time.Second):
		t.Fatal("deadline never fired after resume")
	}
	dl.stop()
}

var _ = ai.Elicitation{}
