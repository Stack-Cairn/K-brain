package tui

import (
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"
)

// listParaParser parses markdown the way glamour does, to find the
// paragraphs inside list items.
var listParaParser = goldmark.New(goldmark.WithExtensions(extension.GFM, extension.DefinitionList)).Parser()

// listParaGap stands in for the blank line between two paragraphs of one
// list item. Glamour draws a list item's paragraphs with nothing between
// them, so "step two" and a continuation paragraph ran together as
// "step twowith". A line holding only a no-break space is not blank to the
// parser, so the paragraphs join into one whose soft breaks glamour keeps;
// the space row is then emptied like any other padding row.
const listParaGap = " "

// splitListParagraphs rewrites the blank lines between consecutive
// paragraphs of a list item so they survive glamour as one empty row. It
// reports whether it changed anything, so the stand-in can be taken out of
// the output.
func splitListParagraphs(s string) (string, bool) {
	if !hasInnerBlankLine(s) {
		return s, false
	}
	src := []byte(s)
	doc := listParaParser.Parse(text.NewReader(src))
	type edit struct {
		from, to int
		repl     string
	}
	var edits []edit
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering || n.Kind() != ast.KindParagraph || n.Parent() == nil || n.Parent().Kind() != ast.KindListItem {
			return ast.WalkContinue, nil
		}
		prev := n.PreviousSibling()
		if prev == nil || (prev.Kind() != ast.KindParagraph && prev.Kind() != ast.KindTextBlock) {
			return ast.WalkContinue, nil
		}
		pl, nl := prev.Lines(), n.Lines()
		if pl.Len() == 0 || nl.Len() == 0 {
			return ast.WalkContinue, nil
		}
		// The blank lines run from the line after prev's last one to the
		// start of n's first line.
		from := pl.At(pl.Len() - 1).Stop
		if i := strings.IndexByte(s[from:], '\n'); i >= 0 {
			from += i + 1
		} else {
			return ast.WalkContinue, nil
		}
		start := nl.At(0).Start
		to := strings.LastIndexByte(s[:start], '\n') + 1
		if to <= from {
			return ast.WalkContinue, nil
		}
		// Keep the next line's lead (indent, quote markers) on the stand-in
		// line so it stays inside the same container.
		lead := s[to:start]
		edits = append(edits, edit{from: from, to: to, repl: lead + listParaGap + "\n"})
		return ast.WalkContinue, nil
	})
	if len(edits) == 0 {
		return s, false
	}
	var b strings.Builder
	b.Grow(len(s) + 8*len(edits))
	pos := 0
	for _, e := range edits {
		b.WriteString(s[pos:e.from])
		b.WriteString(e.repl)
		pos = e.to
	}
	b.WriteString(s[pos:])
	return b.String(), true
}

// hasInnerBlankLine reports whether a line after the first is blank, quote
// markers aside: without one no list item holds two paragraphs.
func hasInnerBlankLine(s string) bool {
	for i := strings.IndexByte(s, '\n'); i >= 0; {
		rest := s[i+1:]
		line, _, more := strings.Cut(rest, "\n")
		if strings.Trim(line, " \t>") == "" && more {
			return true
		}
		j := strings.IndexByte(rest, '\n')
		if j < 0 {
			break
		}
		i += j + 1
	}
	return false
}
