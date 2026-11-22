//go:build tuicap && !windows

package tuicap_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/tui/tuicap"
)

const (
	bsu = "\x1b[?2026h"
	esu = "\x1b[?2026l"
)

// runSyncSession sends one prompt and returns everything kn wrote while the
// reply streamed and settled.
func runSyncSession(t *testing.T, env ...string) []byte {
	t.Helper()
	s := tuicap.Start(t, tuicap.Options{
		Env:     env,
		Replies: []tuicap.Reply{tuicap.Lines(20*time.Millisecond, "synchronized reply one", "synchronized reply two")},
	})
	s.WaitFor("Ask k-brain anything")
	s.Type("hello")
	s.Press(tuicap.Enter)
	s.WaitFor("synchronized reply two")
	s.WaitGone("Esc to interrupt")
	return s.Raw()
}

// With synchronized output on, every frame the renderer writes is one
// BSU … ESU span: they alternate, never nest, and the reply is drawn inside
// one.
func TestSyncOutputBracketsFrames(t *testing.T) {
	raw := runSyncSession(t, "K_BRAIN_SYNC_OUTPUT=1")
	begins, ends := bytes.Count(raw, []byte(bsu)), bytes.Count(raw, []byte(esu))
	if begins == 0 || begins != ends {
		t.Fatalf("got %d BSU and %d ESU; want a matching, non-zero count", begins, ends)
	}
	open := false
	rest := raw
	for {
		b, e := bytes.Index(rest, []byte(bsu)), bytes.Index(rest, []byte(esu))
		if b < 0 && e < 0 {
			break
		}
		if b >= 0 && (e < 0 || b < e) {
			if open {
				t.Fatal("BSU inside an open BSU … ESU span")
			}
			open, rest = true, rest[b+len(bsu):]
			continue
		}
		if !open {
			t.Fatal("ESU without a BSU before it")
		}
		open, rest = false, rest[e+len(esu):]
	}
	if open {
		t.Fatal("the last span is never closed")
	}
	inside := false
	for _, span := range strings.Split(string(raw), bsu)[1:] {
		frame, _, _ := strings.Cut(span, esu)
		if strings.Contains(frame, "synchronized reply two") {
			inside = true
		}
	}
	if !inside {
		t.Fatal("the reply was not drawn inside a synchronized frame")
	}
}

// Turned off — by the override, or because the terminal does not report the
// mode (the emulator answers DECRQM with "not recognised") — no frame is
// bracketed.
func TestSyncOutputAbsentWhenDisabled(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  []string
	}{{"override", []string{"K_BRAIN_SYNC_OUTPUT=0"}}, {"detected", nil}} {
		t.Run(tc.name, func(t *testing.T) {
			raw := runSyncSession(t, tc.env...)
			if bytes.Contains(raw, []byte(bsu)) || bytes.Contains(raw, []byte(esu)) {
				t.Fatalf("synchronized output sequences written while it is off")
			}
			asked := bytes.Contains(raw, []byte("\x1b[?2026$p"))
			if tc.env == nil && !asked {
				t.Fatal("kn should ask the terminal about mode 2026")
			}
			if tc.env != nil && asked {
				t.Fatal("the override should skip the terminal query")
			}
		})
	}
}
