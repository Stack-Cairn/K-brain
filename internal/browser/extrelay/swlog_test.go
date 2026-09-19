package extrelay

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func postLog(r *Relay, text string) int {
	req := httptest.NewRequest(http.MethodPost, "/swlog?token="+r.token, strings.NewReader(text))
	w := httptest.NewRecorder()
	r.handleSWLog(w, req)
	return w.Code
}

func TestSWLogsKeepNewestEntries(t *testing.T) {
	r := &Relay{token: "test"}
	for i := range maxSWLogEntries + 32 {
		if status := postLog(r, fmt.Sprintf("entry %d", i)); status != http.StatusNoContent {
			t.Fatalf("post status = %d", status)
		}
	}
	logs := r.SWLogs()
	if len(logs) != maxSWLogEntries || logs[0] != "entry 32" || logs[len(logs)-1] != fmt.Sprintf("entry %d", maxSWLogEntries+31) {
		t.Fatalf("retained logs = %v", logs)
	}
	var size int
	for _, log := range logs {
		size += len(log)
	}
	if size != r.swlogBytes {
		t.Fatalf("byte count = %d, want %d", r.swlogBytes, size)
	}
	logs[0] = "changed"
	if r.SWLogs()[0] != "entry 32" {
		t.Fatal("snapshot aliases internal logs")
	}
}

func TestSWLogsByteBudget(t *testing.T) {
	r := &Relay{token: "test"}
	const retained = maxSWLogBytes / maxSWLogEntryBytes
	for i := range retained + 5 {
		text := fmt.Sprintf("%04d", i) + strings.Repeat("x", maxSWLogEntryBytes-4)
		if status := postLog(r, text); status != http.StatusNoContent {
			t.Fatalf("post status = %d", status)
		}
	}
	logs := r.SWLogs()
	if len(logs) != retained || !strings.HasPrefix(logs[0], "0005") || r.swlogBytes != maxSWLogBytes {
		t.Fatalf("retention: entries=%d bytes=%d", len(logs), r.swlogBytes)
	}
}

type brokenLogReader struct{}

func (brokenLogReader) Read(p []byte) (int, error) {
	return copy(p, "partial"), errors.New("broken body")
}

func TestSWLogsRejectInvalidBodies(t *testing.T) {
	for _, tc := range []struct {
		name   string
		token  string
		body   io.Reader
		closed bool
		status int
	}{
		{"oversized", "test", strings.NewReader(strings.Repeat("x", maxSWLogEntryBytes+1)), false, http.StatusRequestEntityTooLarge},
		{"broken", "test", brokenLogReader{}, false, http.StatusBadRequest},
		{"unauthorized", "bad", strings.NewReader("secret"), false, http.StatusUnauthorized},
		{"closed", "test", strings.NewReader("late"), true, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &Relay{token: "test", closed: tc.closed}
			req := httptest.NewRequest(http.MethodPost, "/swlog?token="+tc.token, tc.body)
			w := httptest.NewRecorder()
			r.handleSWLog(w, req)
			if w.Code != tc.status || len(r.SWLogs()) != 0 || r.swlogBytes != 0 {
				t.Fatalf("status=%d logs=%v bytes=%d", w.Code, r.SWLogs(), r.swlogBytes)
			}
		})
	}
}

func TestSWLogsConcurrentAppendAndSnapshot(t *testing.T) {
	r := &Relay{token: "test"}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 128 {
				if status := postLog(r, strings.Repeat("x", 8192)); status != http.StatusNoContent {
					t.Errorf("post status = %d", status)
				}
				logs := r.SWLogs()
				var size int
				for _, log := range logs {
					size += len(log)
				}
				if size > maxSWLogBytes || len(logs) > maxSWLogEntries {
					t.Errorf("unbounded logs: entries=%d bytes=%d", len(logs), size)
				}
			}
		})
	}
	wg.Wait()
	if r.swlogBytes != maxSWLogBytes {
		t.Fatalf("retained bytes = %d", r.swlogBytes)
	}
}
