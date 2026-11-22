// Package mermaid draws Mermaid diagram source as terminal text.
//
// It understands a subset of the language: flowcharts (graph / flowchart,
// all four directions, the common node shapes, labelled solid, dotted and
// thick edges, chains, "&" lists and subgraphs) and sequence diagrams
// (participants and actors, messages, self messages, notes, loop / alt /
// opt / par / critical / break frames and autonumber). Everything else is
// reported as unsupported so the caller can show the source instead.
//
// Layout is deterministic: the same source and options always give the same
// lines. Flowcharts are layered (cycles are broken by reversing edges found
// by a depth-first search in source order), ordered by barycentre sweeps and
// placed so that each node sits over the nodes it connects to; edges are
// routed orthogonally through the gaps between layers. Every width is a
// display width, so CJK and other wide text lines up.
package mermaid

import (
	"errors"
	"fmt"
	"strings"
)

// Options tunes a render.
type Options struct {
	// Width is the room in cells; a wider diagram is cut to it and marked
	// Truncated. Zero means unlimited.
	Width int
	// ASCII draws with plain ASCII instead of box-drawing characters.
	ASCII bool
}

// Diagram is a rendered diagram.
type Diagram struct {
	// Kind is "flowchart" or "sequence".
	Kind string
	// Lines are the rows, without trailing blanks.
	Lines []string
	// FullWidth is the drawing's width before any cut.
	FullWidth int
	// Truncated reports that the drawing was cut to Options.Width.
	Truncated bool
}

// UnsupportedError reports a diagram type or feature this package does not
// draw.
type UnsupportedError struct {
	// Kind is the diagram's type as its source names it, e.g. "pie".
	Kind   string
	Reason string
}

func (e *UnsupportedError) Error() string {
	if e.Reason != "" {
		return e.Reason
	}
	if e.Kind == "" {
		return "not a mermaid diagram"
	}
	return e.Kind + " diagrams are not drawn in the terminal"
}

// ParseError reports source this package could not read.
type ParseError struct {
	Line int
	Msg  string
}

func (e *ParseError) Error() string { return fmt.Sprintf("line %d: %s", e.Line, e.Msg) }

// ErrTooLarge reports source past the size or node limits.
var ErrTooLarge = errors.New("diagram too large to draw in the terminal")

const (
	maxSourceBytes = 64 << 10
	maxNodes       = 300
	maxEvents      = 1000
)

// srcLine is one meaningful line of source with its 1-based line number.
type srcLine struct {
	n    int
	text string
}

// sourceLines strips comments, directives and blank lines.
func sourceLines(src string) []srcLine {
	var out []srcLine
	for i, l := range strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n") {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "%%") {
			continue
		}
		out = append(out, srcLine{n: i + 1, text: t})
	}
	return out
}

// skipFrontMatter drops a leading "---" … "---" block (title and config).
func skipFrontMatter(lines []srcLine) []srcLine {
	if len(lines) == 0 || lines[0].text != "---" {
		return lines
	}
	for i := 1; i < len(lines); i++ {
		if lines[i].text == "---" {
			return lines[i+1:]
		}
	}
	return lines
}

// Kind names the diagram type of src from its first statement, "" when
// there is none.
func Kind(src string) string {
	lines := skipFrontMatter(sourceLines(src))
	if len(lines) == 0 {
		return ""
	}
	head := lines[0].text
	if i := strings.IndexAny(head, " \t;"); i >= 0 {
		head = head[:i]
	}
	return head
}

// Render draws src.
func Render(src string, opt Options) (*Diagram, error) {
	if len(src) > maxSourceBytes {
		return nil, ErrTooLarge
	}
	lines := skipFrontMatter(sourceLines(src))
	if len(lines) == 0 {
		return nil, &UnsupportedError{Reason: "empty diagram"}
	}
	kind := Kind(src)
	var d *Diagram
	var err error
	switch strings.ToLower(kind) {
	case "graph", "flowchart", "flowchart-elk":
		d, err = renderFlowchart(lines, opt)
	case "sequencediagram":
		d, err = renderSequence(lines, opt)
	default:
		return nil, &UnsupportedError{Kind: kind}
	}
	if err != nil {
		return nil, err
	}
	d.fit(opt)
	return d, nil
}

// fit cuts the lines to the width, marking the cut.
func (d *Diagram) fit(opt Options) {
	d.FullWidth = 0
	for _, l := range d.Lines {
		d.FullWidth = max(d.FullWidth, width(l))
	}
	if opt.Width <= 0 || d.FullWidth <= opt.Width {
		return
	}
	d.Truncated = true
	tail := "…"
	if opt.ASCII {
		tail = ">"
	}
	for i, l := range d.Lines {
		if width(l) > opt.Width {
			d.Lines[i] = cut(l, opt.Width-width(tail)) + tail
		}
	}
}
