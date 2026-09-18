package tui

import (
	"strings"
	"testing"
)

func TestBgQueryPlain(t *testing.T) {
	if got := bgQuery(false); got != "\x1b]11;?\x1b\\\x1b[?996n\x1b[6n" {
		t.Fatalf("plain query wrong: %q", got)
	}
}

func TestBgQueryTmux(t *testing.T) {
	got := bgQuery(true)
	if !strings.HasPrefix(got, "\x1b]11;?\x1b\\") {
		t.Fatalf("must start with a bare OSC 11 for tmux itself to answer: %q", got)
	}
	if !strings.Contains(got, "\x1bPtmux;\x1b\x1b]11;?\x1b\x1b\\\x1b\\") {
		t.Fatalf("must include the passthrough-wrapped OSC 11 (ESCs doubled): %q", got)
	}
	if !strings.HasSuffix(got, "\x1b[?996n\x1b[6n") {
		t.Fatalf("996 theme query then the CSI 6n terminator must be last and unwrapped: %q", got)
	}
}

func TestFallbackScheme(t *testing.T) {

	if light, ok, _ := fallbackScheme(true, "0;15"); !ok || !light {
		t.Fatal("COLORFGBG 0;15 must resolve light")
	}
	if light, ok, _ := fallbackScheme(false, "15;0"); !ok || light {
		t.Fatal("COLORFGBG 15;0 must resolve dark")
	}

	if _, ok, how := fallbackScheme(true, ""); ok || !strings.Contains(how, "tmux") {
		t.Fatalf("tmux, no signal: must be neutral with a tmux hint, got ok=%v how=%q", ok, how)
	}
	if _, ok, how := fallbackScheme(false, "junk"); ok || !strings.Contains(how, "undetermined") {
		t.Fatalf("no signal: must be neutral, got ok=%v how=%q", ok, how)
	}
}

func TestRuntimeDetectionNeverQueriesTTY(t *testing.T) {
	t.Setenv("K_BRAIN_THEME", "")
	t.Setenv("COLORFGBG", "")
	t.Setenv("TMUX", "1")
	tuiRunning = true
	defer func() { tuiRunning = false; bgCache = bgResult{} }()

	bgCache = bgResult{light: true, valid: true}
	if how := detectColorScheme(); how != "terminal query (cached from startup)" {
		t.Fatalf("runtime detection must reuse the startup query, got %q", how)
	}
	mdMu.Lock()
	light := mdLight
	mdMu.Unlock()
	if !light {
		t.Fatal("cached light answer must apply the light scheme")
	}

	bgCache = bgResult{}
	t.Setenv("COLORFGBG", "15;0")
	if how := detectColorScheme(); !strings.Contains(how, "COLORFGBG") {
		t.Fatalf("runtime detection without cache must use COLORFGBG, got %q", how)
	}

	t.Setenv("COLORFGBG", "")
	if how := detectColorScheme(); !strings.Contains(how, "undetermined") {
		t.Fatalf("runtime detection with no signal must stay neutral, got %q", how)
	}
}
