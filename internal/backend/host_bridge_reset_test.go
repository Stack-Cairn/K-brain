package backend

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
)

// resetAfterRequest accepts one connection, reads the request headers, optionally
// stops listening, and resets the connection without answering.
func resetAfterRequest(t *testing.T, stopListening bool) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		_, _ = http.ReadRequest(bufio.NewReader(c))
		if stopListening {
			_ = ln.Close()
		}
		if tcp, ok := c.(*net.TCPConn); ok {
			_ = tcp.SetLinger(0) // close with RST
		}
		_ = c.Close()
	}()
	return "http://" + ln.Addr().String()
}

func TestHostBridgeResetFromExitedDesktopIsNotConnected(t *testing.T) {
	for _, tc := range []struct {
		name          string
		stopListening bool
		want          string
	}{
		// The desktop exited: nothing listens any more, so the call reports it is gone.
		{"desktop gone", true, hostBridgeNotConnected},
		// The desktop is still there and dropped this one request: keep the failure.
		{"desktop still listening", false, "host bridge request gateway_tunnel_state failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := resetAfterRequest(t, tc.stopListening)
			b := &hostBridge{}
			b.set(&hostBridgeConn{base: base, token: "t", host: "h", tools: []string{"TunnelManager"}, client: newHostBridgeClient()})
			err := b.call(context.Background(), "TunnelManager", "gateway_tunnel_state", nil, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "127.0.0.1") {
				t.Fatalf("err = %v, want %q without the address", err, tc.want)
			}
		})
	}
}
