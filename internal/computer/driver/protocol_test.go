package driver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/computer"
)

type fakeBackend struct {
	revision   string
	calls      int
	fail       bool
	missingAck bool
}

func (b *fakeBackend) Call(ctx context.Context, method string, p Params, out any) error {
	b.calls++
	if err := ctx.Err(); err != nil {
		return err
	}
	if b.fail {
		return &Fault{6, "unsupported"}
	}
	if method == "state" || method == "ax" {
		*out.(*Snapshot) = Snapshot{AppState: computer.AppState{App: text(p, "app"), Elements: []computer.AXElement{}}, Revision: b.revision}
		return nil
	}
	if method == "click" {
		if text(p, "expectedRevision") != b.revision {
			return &Fault{4, "state changed"}
		}
		if b.missingAck {
			return json.Unmarshal([]byte(`{"ok":false}`), out)
		}
		b.revision += "changed"
		return json.Unmarshal([]byte(`{"ok":true}`), out)
	}
	return &Fault{-32601, "unknown"}
}

func request(method string, values map[string]any) Request {
	p := Params{}
	put(p, "token", "secret")
	for k, v := range values {
		put(p, k, v)
	}
	return Request{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: method, Params: p}
}

func TestAuthentication(t *testing.T) {
	b := &fakeBackend{}
	s := New(b, "secret")
	for _, method := range []string{"handshake", "apps", "shutdown", "state", "permissions.status"} {
		_, err := s.dispatch(t.Context(), request(method, map[string]any{"token": "wrong"}))
		var f *Fault
		if !errors.As(err, &f) || f.Code != 8 {
			t.Fatalf("%s: %v", method, err)
		}
	}
	if b.calls != 0 {
		t.Fatal("unauthenticated request reached backend")
	}
	if err := New(b, "").Serve(t.Context(), strings.NewReader(""), &bytes.Buffer{}); err == nil {
		t.Fatal("empty token accepted")
	}
}

func TestGenerationAndExternalChanges(t *testing.T) {
	b := &fakeBackend{revision: "initial"}
	s := New(b, "secret")
	act := request("click", map[string]any{"app": "test", "gen": 1, "index": 0})
	if _, err := s.dispatch(t.Context(), act); err == nil {
		t.Fatal("action without state")
	}
	read := request("state", map[string]any{"app": "test"})
	for range 2 {
		got, err := s.dispatch(t.Context(), read)
		if err != nil || got.(*computer.AppState).Generation != 1 {
			t.Fatalf("%v %v", got, err)
		}
	}
	b.revision = "external"
	if _, err := s.dispatch(t.Context(), act); err == nil {
		t.Fatal("external change not detected")
	}
	got, err := s.dispatch(t.Context(), read)
	if err != nil || got.(*computer.AppState).Generation != 2 {
		t.Fatal(got, err)
	}
	put(act.Params, "gen", 2)
	got, err = s.dispatch(t.Context(), act)
	if err != nil || got.(*computer.AppState).Generation != 3 {
		t.Fatal(got, err)
	}
	if _, err := s.dispatch(t.Context(), act); err == nil {
		t.Fatal("stale generation reused")
	}
}

func TestBackendFailureAndMissingAck(t *testing.T) {
	b := &fakeBackend{revision: "initial"}
	s := New(b, "secret")
	_, err := s.dispatch(t.Context(), request("state", map[string]any{"app": "test"}))
	if err != nil {
		t.Fatal(err)
	}
	act := request("click", map[string]any{"app": "test", "gen": 1, "index": 0})
	b.fail = true
	if _, err = s.dispatch(t.Context(), act); err == nil {
		t.Fatal("failure reported success")
	}
	b.fail = false
	b.missingAck = true
	if _, err = s.dispatch(t.Context(), act); err == nil {
		t.Fatal("missing acknowledgement reported success")
	}
	if s.states["test"].generation != 1 {
		t.Fatal("failed action changed generation")
	}
}

func TestFramesAndCancellation(t *testing.T) {
	var out bytes.Buffer
	input := "not-json\n" + `{"jsonrpc":"2.0","id":"a","method":"handshake","params":{"token":"secret"}}` + "\n" + `{"jsonrpc":"2.0","id":2,"method":"shutdown","params":{"token":"secret"}}` + "\n"
	s := New(&fakeBackend{}, "secret")
	if err := s.Serve(t.Context(), strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 4 || lines[0] != computer.ProtocolVersion {
		t.Fatal(out.String())
	}
	var frame response
	if err := json.Unmarshal([]byte(lines[1]), &frame); err != nil || frame.Error.Code != -32700 {
		t.Fatal(lines[1])
	}
	if err := json.Unmarshal([]byte(lines[2]), &frame); err != nil || string(frame.ID) != `"a"` {
		t.Fatal(lines[2])
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.dispatch(ctx, request("state", map[string]any{"app": "test"})); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
