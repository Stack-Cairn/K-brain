package extrelay

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

func TestConcurrentWebSocketMessages(t *testing.T) {
	nc, peer := net.Pipe()
	defer nc.Close()
	defer peer.Close()
	_ = nc.SetDeadline(time.Now().Add(5 * time.Second))
	_ = peer.SetDeadline(time.Now().Add(5 * time.Second))
	c := &conn{nc: nc, r: nc}
	const writers, messages = 16, 64
	done := make(chan error, writers)
	for writer := range writers {
		go func() {
			for i := range messages {
				payload := fmt.Sprintf("%03d:%03d:", writer, i) + string(bytes.Repeat([]byte("x"), 256))
				if err := c.writeText([]byte(payload)); err != nil {
					done <- err
					return
				}
			}
			done <- nil
		}()
	}
	rd := wsutil.Reader{Source: peer, State: ws.StateClientSide, CheckUTF8: true, MaxFrameSize: 1024}
	seen := make(map[string]bool)
	for range writers * messages {
		header, err := rd.NextFrame()
		if err != nil {
			t.Fatalf("interleaved WebSocket header: %v", err)
		}
		payload, err := io.ReadAll(&rd)
		if err != nil || header.OpCode != ws.OpText || len(payload) != 264 {
			t.Fatalf("interleaved WebSocket payload: opcode=%v bytes=%d err=%v", header.OpCode, len(payload), err)
		}
		if !bytes.Equal(payload[8:], bytes.Repeat([]byte("x"), 256)) || seen[string(payload)] {
			t.Fatal("corrupted or duplicate WebSocket message")
		}
		seen[string(payload)] = true
	}
	for range writers {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

type memorySocket struct {
	net.Conn
	output bytes.Buffer
	closed bool
}

func (c *memorySocket) Write(p []byte) (int, error)      { return c.output.Write(p) }
func (c *memorySocket) SetWriteDeadline(time.Time) error { return nil }
func (c *memorySocket) Close() error                     { c.closed = true; return nil }

func maskedFrame(op ws.OpCode, fin bool, text string) ws.Frame {
	return ws.MaskFrameInPlace(ws.NewFrame(op, fin, []byte(text)))
}

func TestWebSocketMessageLimits(t *testing.T) {
	const limit = 256
	for _, tc := range []struct {
		name    string
		frames  []ws.Frame
		want    string
		wantErr error
	}{
		{"exact", []ws.Frame{maskedFrame(ws.OpText, true, string(bytes.Repeat([]byte("a"), limit)))}, string(bytes.Repeat([]byte("a"), limit)), nil},
		{"oversized", []ws.Frame{maskedFrame(ws.OpText, true, string(bytes.Repeat([]byte("a"), limit+1)))}, "", errMessageTooLarge},
		{"fragmented_exact", []ws.Frame{maskedFrame(ws.OpText, false, string(bytes.Repeat([]byte("a"), 200))), maskedFrame(ws.OpContinuation, true, string(bytes.Repeat([]byte("b"), 56)))}, string(bytes.Repeat([]byte("a"), 200)) + string(bytes.Repeat([]byte("b"), 56)), nil},
		{"fragmented_oversized", []ws.Frame{maskedFrame(ws.OpText, false, string(bytes.Repeat([]byte("a"), 200))), maskedFrame(ws.OpContinuation, true, string(bytes.Repeat([]byte("b"), 57)))}, "", errMessageTooLarge},
		{"fragmented_control", []ws.Frame{maskedFrame(ws.OpText, false, "part"), maskedFrame(ws.OpPing, true, "ping"), maskedFrame(ws.OpContinuation, true, "two")}, "parttwo", nil},
		{"fragmented_utf8", []ws.Frame{maskedFrame(ws.OpText, false, "\xe6"), maskedFrame(ws.OpContinuation, true, "\xb0\xaa脑")}, "氪脑", nil},
		{"invalid_utf8", []ws.Frame{maskedFrame(ws.OpText, true, "\xff")}, "", wsutil.ErrInvalidUTF8},
		{"truncated_fragment", []ws.Frame{maskedFrame(ws.OpText, false, "part")}, "", io.ErrUnexpectedEOF},
		{"binary_ignored", []ws.Frame{maskedFrame(ws.OpBinary, false, "binary"), maskedFrame(ws.OpContinuation, true, "data"), maskedFrame(ws.OpText, true, "text")}, "text", nil},
		{"binary_oversized", []ws.Frame{maskedFrame(ws.OpBinary, false, string(bytes.Repeat([]byte("a"), 200))), maskedFrame(ws.OpContinuation, true, string(bytes.Repeat([]byte("b"), 57)))}, "", errMessageTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var input bytes.Buffer
			for _, frame := range tc.frames {
				if err := ws.WriteFrame(&input, frame); err != nil {
					t.Fatal(err)
				}
			}
			nc := &memorySocket{}
			c := &conn{nc: nc, r: &input}
			payload, err := c.readTextLimit(limit)
			if !errors.Is(err, tc.wantErr) || string(payload) != tc.want {
				t.Fatalf("readText = %q, %v; want %q, %v", payload, err, tc.want, tc.wantErr)
			}
			if nc.closed != (tc.wantErr != nil) {
				t.Fatal("read failure did not close the socket")
			}
			if tc.name == "fragmented_control" {
				pong, err := ws.ReadFrame(&nc.output)
				if err != nil || pong.Header.OpCode != ws.OpPong || string(pong.Payload) != "ping" {
					t.Fatalf("pong = %+v, %v", pong, err)
				}
			}
		})
	}
}

func TestWebSocketRejectsOversizedHeaderBeforePayload(t *testing.T) {
	var input bytes.Buffer
	if err := ws.WriteHeader(&input, ws.Header{Fin: true, OpCode: ws.OpText, Masked: true, Length: maxMessageBytes + 1}); err != nil {
		t.Fatal(err)
	}
	c := &conn{nc: &memorySocket{}, r: &input}
	if _, err := c.readText(); !errors.Is(err, errMessageTooLarge) {
		t.Fatalf("oversized header = %v", err)
	}
}

func TestWebSocketPingAndConcurrentText(t *testing.T) {
	nc, peer := net.Pipe()
	defer nc.Close()
	defer peer.Close()
	_ = nc.SetDeadline(time.Now().Add(5 * time.Second))
	_ = peer.SetDeadline(time.Now().Add(5 * time.Second))
	c := &conn{nc: nc, r: nc}
	const count = 128
	done := make(chan error, 3)
	go func() {
		for range count {
			if err := c.writeText([]byte("reply")); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	go func() {
		payload, err := c.readText()
		if err == nil && string(payload) != "done" {
			err = fmt.Errorf("unexpected read result %q", payload)
		}
		done <- err
	}()
	go func() {
		for range count {
			if err := wsutil.WriteClientMessage(peer, ws.OpPing, []byte("heartbeat")); err != nil {
				done <- err
				return
			}
		}
		done <- wsutil.WriteClientText(peer, []byte("done"))
	}()
	rd := wsutil.Reader{Source: peer, State: ws.StateClientSide, MaxFrameSize: 128}
	var texts, pongs int
	for range count * 2 {
		header, err := rd.NextFrame()
		if err != nil {
			t.Fatal(err)
		}
		payload, err := io.ReadAll(&rd)
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case header.OpCode == ws.OpText && string(payload) == "reply":
			texts++
		case header.OpCode == ws.OpPong && string(payload) == "heartbeat":
			pongs++
		default:
			t.Fatalf("corrupt frame: opcode=%v payload=%q", header.OpCode, payload)
		}
	}
	if texts != count || pongs != count {
		t.Fatalf("text=%d pong=%d", texts, pongs)
	}
	for range 3 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestWebSocketCloseHandshake(t *testing.T) {
	var input bytes.Buffer
	if err := ws.WriteFrame(&input, maskedFrame(ws.OpClose, true, string(ws.NewCloseFrameBody(ws.StatusNormalClosure, "done")))); err != nil {
		t.Fatal(err)
	}
	nc := &memorySocket{}
	c := &conn{nc: nc, r: &input}
	_, err := c.readText()
	var closed wsutil.ClosedError
	if !errors.As(err, &closed) || closed.Code != ws.StatusNormalClosure || !nc.closed {
		t.Fatalf("close = %v", err)
	}
	reply, err := ws.ReadFrame(&nc.output)
	if err != nil || reply.Header.OpCode != ws.OpClose {
		t.Fatalf("close reply = %+v, %v", reply, err)
	}
}

type failingSocket struct {
	net.Conn
	writes int
}

func (c *failingSocket) Write(p []byte) (int, error) {
	c.writes++
	return len(p) / 2, io.ErrUnexpectedEOF
}

func TestWebSocketWriteFailureClosesSocket(t *testing.T) {
	nc, peer := net.Pipe()
	defer nc.Close()
	defer peer.Close()
	broken := &failingSocket{Conn: nc}
	c := &conn{nc: broken, r: nc}
	if err := c.writeText([]byte("reply")); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("write failure = %v", err)
	}
	if err := c.writeText([]byte("next")); err == nil || broken.writes != 1 {
		t.Fatalf("write resumed on corrupt connection: writes=%d err=%v", broken.writes, err)
	}
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := peer.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("socket not closed: %v", err)
	}
}

type shortDeadlineSocket struct {
	net.Conn
	requested time.Time
}

func (c *shortDeadlineSocket) SetWriteDeadline(deadline time.Time) error {
	c.requested = deadline
	if !deadline.IsZero() {
		deadline = time.Now().Add(20 * time.Millisecond)
	}
	return c.Conn.SetWriteDeadline(deadline)
}

func TestWebSocketBlockedWriteTimesOut(t *testing.T) {
	nc, peer := net.Pipe()
	defer nc.Close()
	defer peer.Close()
	socket := &shortDeadlineSocket{Conn: nc}
	c := &conn{nc: socket, r: nc}
	start := time.Now()
	err := c.writeText([]byte("unread reply"))
	var timeout net.Error
	if !errors.As(err, &timeout) || !timeout.Timeout() {
		t.Fatalf("blocked write = %v", err)
	}
	if time.Since(start) > 2*time.Second || socket.requested.Sub(start) < frameWriteTimeout {
		t.Fatal("write deadline was not applied")
	}
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := peer.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("timed-out socket not closed: %v", err)
	}
}
