package backend

import (
	"bytes"
	"net/http"
	"time"
)

// Handlers often write their reply while still holding a session lock (a
// deferred rt.mu.Unlock runs after writeJSON). A client that stops reading —
// a stalled desktop, a suspended web view — then blocks that write on a full
// socket buffer with the lock held, and every request, run and event stream of
// the session queues behind it; one lock further (s.mu) the whole backend
// stops answering. deferredResponse holds the reply in memory until the
// handler has returned and its locks are released, then writes it with a
// deadline.
//
// Streaming handlers (SSE) call Flush: from then on writes go straight to the
// connection, as before. A reply larger than deferredResponseMax also streams.
type deferredResponse struct {
	w         http.ResponseWriter
	status    int
	buf       bytes.Buffer
	streaming bool
}

const (
	deferredResponseMax = 32 << 20
	// deferredWriteTimeout bounds writing a held reply to a client that does
	// not read it.
	deferredWriteTimeout = 2 * time.Minute
)

func newDeferredResponse(w http.ResponseWriter) *deferredResponse {
	return &deferredResponse{w: w}
}

func (d *deferredResponse) Header() http.Header { return d.w.Header() }

func (d *deferredResponse) WriteHeader(status int) {
	if d.streaming {
		d.w.WriteHeader(status)
		return
	}
	if d.status == 0 {
		d.status = status
	}
}

func (d *deferredResponse) Write(p []byte) (int, error) {
	if d.streaming {
		return d.w.Write(p)
	}
	if d.status == 0 {
		d.status = http.StatusOK
	}
	if d.buf.Len()+len(p) > deferredResponseMax {
		if err := d.startStreaming(); err != nil {
			return 0, err
		}
		return d.w.Write(p)
	}
	return d.buf.Write(p)
}

// Flush switches to streaming: what is held is written and later writes go
// straight through.
func (d *deferredResponse) Flush() {
	if err := d.startStreaming(); err != nil {
		return
	}
	if f, ok := d.w.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the connection's own controls.
func (d *deferredResponse) Unwrap() http.ResponseWriter { return d.w }

func (d *deferredResponse) startStreaming() error {
	if d.streaming {
		return nil
	}
	d.streaming = true
	if d.status != 0 {
		d.w.WriteHeader(d.status)
	}
	if d.buf.Len() == 0 {
		return nil
	}
	_, err := d.w.Write(d.buf.Bytes())
	d.buf = bytes.Buffer{}
	return err
}

// finish writes a held reply once the handler has returned.
func (d *deferredResponse) finish() {
	if d.streaming || d.status == 0 && d.buf.Len() == 0 {
		return
	}
	_ = http.NewResponseController(d.w).SetWriteDeadline(time.Now().Add(deferredWriteTimeout))
	d.w.WriteHeader(d.status)
	_, _ = d.w.Write(d.buf.Bytes())
}
