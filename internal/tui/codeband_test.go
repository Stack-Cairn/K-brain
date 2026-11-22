package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

func TestCodeBandHasNoHoles(t *testing.T) {
	restore := useProfile(t, termenv.TrueColor, true)
	defer restore()
	src := "```go\nfunc f() {\n\tx := 1\n\n\treturn x\n}\n```"
	for name, out := range map[string]string{
		"stream":   strings.Join(assistantRows(nil, src, 60), "\n"),
		"block":    renderBlock(block{kind: blockAssistant, text: src}, 60),
		"markdown": renderMarkdown(src, 60),
	} {
		rows := strings.Split(out, "\n")
		blankAt := -1
		for i, r := range rows {
			if strings.Contains(ansi.Strip(r), "x := 1") {
				blankAt = i + 1
			}
		}
		if blankAt < 0 || blankAt >= len(rows) {
			t.Fatalf("%s: code rows not found:\n%s", name, ansi.Strip(out))
		}
		if !paintsBackground(rows[blankAt]) {
			t.Errorf("%s: the band's blank line lost its background: %q", name, rows[blankAt])
		}
	}
}

func TestCodeBandMarksWrappedLines(t *testing.T) {
	for _, ascii := range []bool{false, true} {
		setASCIIGlyphs(ascii)
		out := ansi.Strip(renderCodeBlock("abcdefghijklmnopqrstuvwxyz\nshort", "", 16))
		setASCIIGlyphs(false)
		rows := strings.Split(out, "\n")
		marked := 0
		for _, r := range rows {
			trim := strings.TrimLeft(r, " ")
			if strings.HasPrefix(trim, asciiOr(ascii, unicodeGlyphs.Wrap, asciiGlyphs.Wrap)+" ") {
				marked++
			}
			if strings.Contains(r, "short") && strings.Contains(r, asciiOr(ascii, unicodeGlyphs.Wrap, asciiGlyphs.Wrap)) {
				t.Errorf("an unwrapped line carries the wrap mark: %q", r)
			}
		}
		if marked == 0 {
			t.Errorf("ascii=%v: wrapped rows carry no continuation mark:\n%s", ascii, out)
		}
	}
}

func asciiOr(ascii bool, u, a string) string {
	if ascii {
		return a
	}
	return u
}

func TestPaintsBackground(t *testing.T) {
	for in, want := range map[string]bool{
		"plain":                         false,
		"\x1b[48;2;1;2;3m  \x1b[0m":     true,
		"\x1b[38;2;48;48;48mx\x1b[0m":   false,
		"\x1b[38;5;48mx\x1b[0m":         false,
		"\x1b[1;44mx\x1b[0m":            true,
		"\x1b[104m \x1b[0m":             true,
		"\x1b[38;5;1;48;5;236m \x1b[0m": true,
	} {
		if got := paintsBackground(in); got != want {
			t.Errorf("paintsBackground(%q) = %v, want %v", in, got, want)
		}
	}
}
