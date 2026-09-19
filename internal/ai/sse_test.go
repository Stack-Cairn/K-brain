package ai

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestSSEFraming(t *testing.T) {
	r := newSSEReader(strings.NewReader(": heartbeat\r\nevent: ignored\r\n\r\nevent: delta\r\nid: 1\r\nretry: 10\r\ndata: {\r\ndata: \"text\":\"hello\"}\r\n\r\ndata: {}\r\n\r\n"))
	ev, ok := r.next()
	if !ok || ev.Type != "delta" || ev.Data != "{\n\"text\":\"hello\"}" {
		t.Fatalf("first event: %+v, %v", ev, r.err)
	}
	ev, ok = r.next()
	if !ok || ev.Type != "" || ev.Data != "{}" {
		t.Fatalf("second event: %+v, %v", ev, r.err)
	}
	if _, ok := r.next(); ok || r.err != nil {
		t.Fatalf("end: %v, %v", ok, r.err)
	}
}

func TestSSERejectsTruncatedAndOversizedEvents(t *testing.T) {
	for name, input := range map[string]string{
		"truncated":   "data: {}\n",
		"long line":   "data: " + strings.Repeat("x", maxSSEEventSize),
		"large frame": strings.Repeat("data: "+strings.Repeat("x", 1024)+"\n", maxSSEEventSize/1024+1) + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			r := newSSEReader(strings.NewReader(input))
			if _, ok := r.next(); ok || r.err == nil {
				t.Fatalf("accepted invalid event: %v", r.err)
			}
			if name == "truncated" && !errors.Is(r.err, io.ErrUnexpectedEOF) {
				t.Fatal(r.err)
			}
		})
	}
}
