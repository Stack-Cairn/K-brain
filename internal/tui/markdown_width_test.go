package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestRenderedLinesNeverExceedWidth(t *testing.T) {
	src := "some **text** here and `code`\n\n- item one\n- item two\n\n```\nx := " + strings.Repeat("y", 60) + "\n```\n\nplain " + strings.Repeat("word ", 30)
	for _, w := range []int{8, 20, 40, 58, 80} {
		out := renderMarkdown(src, w)
		for i, l := range strings.Split(out, "\n") {
			if got := ansi.StringWidth(l); got > w {
				t.Errorf("width %d: line %d is %d wide: %q", w, i, got, ansi.Strip(l))
			}
		}
	}
}
