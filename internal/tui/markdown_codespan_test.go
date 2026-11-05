package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// An inline code span is one word: glamour's word wrap must not break it after
// a hyphen, with or without colour.
func TestInlineCodeSpanStaysWhole(t *testing.T) {
	src := "Tests pass; `vet` flagged the unused variable, and the edit above swaps it for the new `--json` flag, not `go-vet -x`."
	for _, mono := range []bool{false, true} {
		inlineCodeSpanWidths(t, src, mono)
	}
}

func inlineCodeSpanWidths(t *testing.T, src string, mono bool) {
	t.Helper()
	profile := termenv.TrueColor
	if mono {
		profile = termenv.Ascii
	}
	restore := useProfile(t, profile, true)
	defer restore()
	if mono {
		setMonoMode(true, true)
	}
	for w := 24; w <= 110; w++ {
		out := renderMarkdown(src, w)
		plain := ansi.Strip(out)
		if strings.Contains(out, heldHyphen) {
			t.Fatalf("mono=%v w=%d: placeholder hyphen leaked:\n%s", mono, w, plain)
		}
		for _, span := range []string{"--json", "go-vet"} {
			if !strings.Contains(plain, span) {
				t.Fatalf("mono=%v w=%d: span %q broken:\n%s", mono, w, span, plain)
			}
		}
		for _, l := range strings.Split(out, "\n") {
			if got := ansi.StringWidth(l); got > w {
				t.Fatalf("mono=%v w=%d: line is %d wide: %q", mono, w, got, ansi.Strip(l))
			}
		}
	}
}

// Hyphens outside code spans, and an existing non-breaking hyphen, are left
// as written.
func TestHoldCodeHyphensOnlyTouchesCodeSpans(t *testing.T) {
	if got, held := holdCodeHyphens("a well-known `x-y` flag"); !held || got != "a well-known `x"+heldHyphen+"y` flag" {
		t.Errorf("got %q held=%v", got, held)
	}
	src := "already " + heldHyphen + " here `x-y`"
	if got, held := holdCodeHyphens(src); held || got != src {
		t.Errorf("text with a non-breaking hyphen changed: %q", got)
	}
}
