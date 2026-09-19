package extrelay

import (
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

const maxMessageBytes = 64 << 20
const frameWriteTimeout = 10 * time.Second

var errMessageTooLarge = errors.New("websocket message exceeds size limit")

type conn struct {
	nc net.Conn
	r  io.Reader
	wm sync.Mutex
}

func (c *conn) close() { _ = c.nc.Close() }

func (c *conn) writeFrame(write func(io.Writer) error) error {
	c.wm.Lock()
	defer c.wm.Unlock()
	if err := c.nc.SetWriteDeadline(time.Now().Add(frameWriteTimeout)); err != nil {
		c.close()
		return err
	}
	if err := write(c.nc); err != nil {
		c.close()
		return err
	}
	if err := c.nc.SetWriteDeadline(time.Time{}); err != nil {
		c.close()
		return err
	}
	return nil
}

func (c *conn) writeText(b []byte) error {
	return c.writeFrame(func(w io.Writer) error {
		return wsutil.WriteServerMessage(w, ws.OpText, b)
	})
}

func (c *conn) handleControlFrame(h ws.Header, src io.Reader) error {
	payload, err := io.ReadAll(src)
	if err != nil {
		return err
	}
	if int64(len(payload)) != h.Length {
		return io.ErrUnexpectedEOF
	}
	if h.OpCode == ws.OpPong {
		return nil
	}
	return c.writeFrame(func(w io.Writer) error {
		return wsutil.HandleClientControlMessage(w, wsutil.Message{OpCode: h.OpCode, Payload: payload})
	})
}

func (c *conn) readText() ([]byte, error) {
	return c.readTextLimit(maxMessageBytes)
}

func (c *conn) readTextLimit(limit int64) (payload []byte, err error) {
	defer func() {
		if err != nil {
			c.close()
		}
	}()
	rd := wsutil.Reader{
		Source: c.r, State: ws.StateServerSide, CheckUTF8: true,
		MaxFrameSize: limit, OnIntermediate: c.handleControlFrame,
	}
	for {
		header, err := rd.NextFrame()
		if errors.Is(err, wsutil.ErrFrameTooLarge) {
			return nil, errMessageTooLarge
		}
		if err != nil {
			return nil, err
		}
		if header.OpCode.IsControl() {
			if err := c.handleControlFrame(header, &rd); err != nil {
				return nil, err
			}
			continue
		}
		var size int64
		if header.OpCode == ws.OpText {
			payload, err = io.ReadAll(io.LimitReader(&rd, limit+1))
			size = int64(len(payload))
		} else {
			size, err = io.Copy(io.Discard, io.LimitReader(&rd, limit+1))
		}
		if size > limit || errors.Is(err, wsutil.ErrFrameTooLarge) {
			return nil, errMessageTooLarge
		}
		if err != nil {
			return nil, err
		}
		if header.OpCode == ws.OpText {
			return payload, nil
		}
	}
}
