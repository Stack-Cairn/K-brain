package extrelay

import (
	"errors"
	"io"
	"net/http"
	"time"
)

const (
	maxSWLogEntryBytes = 64 << 10
	maxSWLogBytes      = 1 << 20
	maxSWLogEntries    = 256
)

func (r *Relay) SWLogs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.swlogs...)
}

func (r *Relay) handleSWLog(w http.ResponseWriter, req *http.Request) {
	if req.URL.Query().Get("token") != r.token {
		http.Error(w, "bad token", http.StatusUnauthorized)
		return
	}
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(5 * time.Second))
	defer controller.SetReadDeadline(time.Time{})
	body, err := io.ReadAll(http.MaxBytesReader(w, req.Body, maxSWLogEntryBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		status := http.StatusBadRequest
		if errors.As(err, &tooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		http.Error(w, "cannot read log entry", status)
		return
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		http.Error(w, "relay closed", http.StatusServiceUnavailable)
		return
	}
	r.swlogs = append(r.swlogs, string(body))
	r.swlogBytes += len(body)
	for len(r.swlogs) > maxSWLogEntries || r.swlogBytes > maxSWLogBytes {
		r.swlogBytes -= len(r.swlogs[0])
		r.swlogs[0] = ""
		r.swlogs = r.swlogs[1:]
	}
	r.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}
