package tui

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestClickWheelMouseEscapes(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	enableClickWheelMouse(w)
	w.Close()
	buf := make([]byte, 256)
	_ = r.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _ := r.Read(buf)
	got := string(buf[:n])

	if !strings.Contains(got, "\x1b[?1006h") {
		t.Errorf("must enable SGR coords ?1006h, got %q", got)
	}
	if !strings.Contains(got, "\x1b[?1002h") {
		t.Errorf("must enable button-motion ?1002h so a drag reports motion (in-app selection), got %q", got)
	}

	if strings.Contains(got, "\x1b[?1000h") {
		t.Errorf("must NOT enable ?1000h (it downgrades ?1002 button-motion tracking), got %q", got)
	}
	if strings.Contains(got, "1003") {
		t.Errorf("must NOT enable any-motion ?1003 (passive moves stay silent), got %q", got)
	}
}

func TestDisableClickWheelMouse(t *testing.T) {
	r, w, _ := os.Pipe()
	disableClickWheelMouse(w)
	w.Close()
	buf := make([]byte, 256)
	_ = r.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _ := r.Read(buf)
	got := string(buf[:n])
	if !strings.Contains(got, "\x1b[?1000l") || !strings.Contains(got, "\x1b[?1006l") || !strings.Contains(got, "\x1b[?1002l") {
		t.Errorf("must release ?1000, ?1002 and ?1006, got %q", got)
	}
}
