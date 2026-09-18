package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

func TestReadFrameSplit(t *testing.T) {

	wire := "Content-Length: 9\r\n\r\n{\"a\":\"b\"}Content-Length: 8\r\n\r\n{\"c\":42}"
	br := bufio.NewReader(strings.NewReader(wire))
	body, err := readFrame(br)
	if err != nil || string(body) != `{"a":"b"}` {
		t.Fatalf("frame 1: %q %v", body, err)
	}
	body, err = readFrame(br)
	if err != nil || string(body) != `{"c":42}` {
		t.Fatalf("frame 2: %q %v", body, err)
	}
}

func TestReadFrameBad(t *testing.T) {
	if _, err := readFrame(bufio.NewReader(strings.NewReader("Content-Length: nope\r\n\r\n"))); err == nil {
		t.Fatal("bad length should error")
	}
	if _, err := readFrame(bufio.NewReader(strings.NewReader("\r\n"))); err == nil {
		t.Fatal("missing length should error")
	}
	if _, err := readFrame(bufio.NewReader(strings.NewReader("Content-Length: 10\r\n\r\nshort"))); err == nil {
		t.Fatal("short body should error")
	}
}

func TestRequestRoutingAndCancel(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	defer inW.Close()
	defer outW.Close()
	c := newClient(inW, outR, nil)
	defer c.shutdown()

	go func() {
		br := bufio.NewReader(inR)
		body, err := readFrame(br)
		if err != nil {
			return
		}
		var msg rpcMessage
		_ = json.Unmarshal(body, &msg)
		resp, _ := json.Marshal(rpcMessage{ID: msg.ID, Result: json.RawMessage(`{"ok":true}`)})
		_, _ = fmt.Fprintf(outW, "Content-Length: %d\r\n\r\n%s", len(resp), resp)
	}()

	var res struct {
		Ok bool `json:"ok"`
	}
	if err := c.request(context.Background(), "test/method", map[string]any{"x": 1}, &res); err != nil || !res.Ok {
		t.Fatalf("request 1: %v %+v", err, res)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := c.request(ctx, "test/never", nil, nil); err == nil {
		t.Fatal("unanswered request should hit ctx deadline")
	}
}

func TestServerRequestsGetNullAck(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	defer inW.Close()
	defer outW.Close()
	c := newClient(inW, outR, nil)
	defer c.shutdown()

	ack := make(chan string, 1)
	go func() {
		br := bufio.NewReader(inR)
		for {
			body, err := readFrame(br)
			if err != nil {
				return
			}
			var msg rpcMessage
			_ = json.Unmarshal(body, &msg)
			if len(msg.ID) > 0 && msg.Method == "" {
				ack <- string(msg.ID)
				return
			}
		}
	}()
	req := `{"jsonrpc":"2.0","id":99,"method":"window/workDoneProgress/create","params":{}}`
	_, _ = fmt.Fprintf(outW, "Content-Length: %d\r\n\r\n%s", len(req), req)
	select {
	case <-ack:
	case <-time.After(2 * time.Second):
		t.Fatal("no ack for server request")
	}
}

func TestErrorResponseAndGarbageFrame(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	defer inW.Close()
	defer outW.Close()
	c := newClient(inW, outR, nil)
	defer c.shutdown()

	go func() {
		br := bufio.NewReader(inR)
		body, err := readFrame(br)
		if err != nil {
			return
		}
		var msg rpcMessage
		_ = json.Unmarshal(body, &msg)

		_, _ = fmt.Fprint(outW, "Content-Length: 5\r\n\r\n{no:}")
		resp, _ := json.Marshal(rpcMessage{ID: msg.ID, Error: &rpcError{Code: -32602, Message: "bad params"}})
		_, _ = fmt.Fprintf(outW, "Content-Length: %d\r\n\r\n%s", len(resp), resp)
	}()

	err := c.request(context.Background(), "test/method", nil, nil)
	var rpcErr *rpcError
	if !errors.As(err, &rpcErr) || rpcErr.Code != -32602 {
		t.Fatalf("want rpc error, got %v", err)
	}
}

func TestUnmarshalableParams(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	defer inW.Close()
	defer outW.Close()
	c := newClient(inW, outR, nil)
	defer c.shutdown()

	if err := c.request(context.Background(), "test/method", make(chan int), nil); err == nil {
		t.Error("unmarshalable request params must error")
	}
	c.notify("test/note", make(chan int))
	c.send(rpcMessage{Method: "test/bad", Params: json.RawMessage("{not json")})

	got := make(chan struct{})
	go func() {
		if _, err := readFrame(bufio.NewReader(inR)); err == nil {
			close(got)
		}
	}()
	select {
	case <-got:
		t.Error("a broken message reached the wire")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestRequestAfterShutdown(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	defer inW.Close()
	defer outW.Close()
	c := newClient(inW, outR, nil)
	go func() {
		br := bufio.NewReader(inR)
		for {
			if _, err := readFrame(br); err != nil {
				return
			}
		}
	}()

	done := make(chan error, 1)
	go func() { done <- c.request(context.Background(), "test/never", nil, nil) }()
	time.Sleep(50 * time.Millisecond)
	c.shutdown()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "connection closed") {
			t.Fatalf("in-flight request after shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("request did not unblock on shutdown")
	}

	if err := c.request(context.Background(), "test/after", nil, nil); err == nil {
		t.Error("request on a dead client must error")
	}
	c.send(rpcMessage{Method: "test/after"})
}

func TestRPCErrorMessage(t *testing.T) {
	err := &rpcError{Code: -32601, Message: "method not found"}
	if got := err.Error(); got != "rpc error -32601: method not found" {
		t.Fatalf("Error() = %q", got)
	}
}
