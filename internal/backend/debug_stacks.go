package backend

import (
	"context"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/stackdump"
)

// debugStacksMu lets one dump run at a time; a second request while one is
// being written gets 429 instead of a pile of identical files.
var debugStacksMu sync.Mutex

// writeStacks is replaced in tests.
var writeStacks = stackdump.Write

// debugStacks writes every goroutine's stack to the dump directory without
// stopping the process. It takes none of the server's locks, so it answers
// while the rest of the backend is wedged — which is when it is needed.
// Only loopback clients may ask: a dump names files and sessions.
func (s *Server) debugStacks(w http.ResponseWriter, r *http.Request) {
	if !loopbackRemote(r.RemoteAddr) {
		writeJSONError(w, http.StatusForbidden, "stack dumps are only served to loopback clients")
		return
	}
	if !debugStacksMu.TryLock() {
		writeJSONError(w, http.StatusTooManyRequests, "a stack dump is already being written")
		return
	}
	defer debugStacksMu.Unlock()
	path, err := writeStacks("GET /v1/debug/stacks")
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": path})
}

func loopbackRemote(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Watchdog reports a backend that stopped making progress: every interval it
// takes the server lock, then each loaded session's lock, and builds the
// health reply. One of those not finishing within timeout writes a stack dump
// (once per stall) and logs a warning to k-brain.log.
func (s *Server) Watchdog(timeout time.Duration) *stackdump.Watchdog {
	return &stackdump.Watchdog{
		Name:     "backend (server lock, session locks, health)",
		Interval: 2 * time.Second,
		Timeout:  timeout,
		Probe:    s.watchdogProbe,
	}
}

func (s *Server) watchdogProbe(ctx context.Context) {
	s.mu.Lock()
	runtimes := make([]*runtimeSession, 0, len(s.sessions))
	for _, rt := range s.sessions {
		runtimes = append(runtimes, rt)
	}
	s.mu.Unlock()
	// Lock order is rt.mu, then s.mu: each session lock is taken after the
	// server lock is released.
	for _, rt := range runtimes {
		if ctx.Err() != nil {
			return
		}
		rt.mu.Lock()
		//nolint:staticcheck // only probing that the lock is free
		rt.mu.Unlock()
	}
	_ = s.health()
}
