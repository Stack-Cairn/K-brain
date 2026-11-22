package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestListItemParagraphsStaySeparate(t *testing.T) {
	src := "1. step one\n2. step two\n\n   with an indented continuation paragraph."
	for name, out := range map[string]string{
		"stream": strings.Join(markdownRows(nil, src, 60), "\n"),
		"whole":  renderMarkdownWhole(src, 60),
	} {
		plain := ansi.Strip(out)
		if strings.Contains(plain, "twowith") {
			t.Fatalf("%s: paragraphs ran together:\n%s", name, plain)
		}
		if !strings.Contains(plain, "step two\n\n") || !strings.Contains(plain, "with an indented") {
			t.Fatalf("%s: want a blank row between the item's paragraphs:\n%s", name, plain)
		}
		if strings.Contains(out, listParaGap) {
			t.Fatalf("%s: stand-in left in the output: %q", name, out)
		}
	}
}

func TestSplitListParagraphs(t *testing.T) {
	cases := []struct{ in, want string }{
		{"- a\n\n  b", "- a\n  " + listParaGap + "\n  b"},
		{"- a\n\n\n  b\n\n  c", "- a\n  " + listParaGap + "\n  b\n  " + listParaGap + "\n  c"},
		{"> - a\n>\n>   b", "> - a\n>   " + listParaGap + "\n>   b"},
		// Not inside a list item: untouched.
		{"a\n\nb", "a\n\nb"},
		{"- a\n- b\n\nafter", "- a\n- b\n\nafter"},
		// A code block inside the item is left alone.
		{"- a\n\n  ```\n  x\n\n  y\n  ```", "- a\n\n  ```\n  x\n\n  y\n  ```"},
	}
	for _, c := range cases {
		got, _ := splitListParagraphs(c.in)
		if got != c.want {
			t.Errorf("splitListParagraphs(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
