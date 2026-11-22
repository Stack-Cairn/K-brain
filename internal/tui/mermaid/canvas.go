package mermaid

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
)

// Line directions a cell connects to. A line cell's glyph is chosen from the
// set of directions every line through it adds, so crossings and junctions
// draw themselves.
const (
	dirUp uint8 = 1 << iota
	dirDown
	dirLeft
	dirRight
)

// lineStyle is how an edge's straight runs are drawn.
type lineStyle uint8

const (
	styleSolid lineStyle = iota
	styleDotted
	styleThick
)

// cell is one terminal cell of the canvas.
type cell struct {
	// glyph overrides the line mask when set: text, arrowheads, corners.
	glyph string
	// cont marks the second cell of a wide glyph; it draws nothing.
	cont bool
	mask uint8
	// box marks node and frame borders, which keep light lines whatever
	// edge joins them; sharp keeps a box corner square.
	box, sharp, round bool
	style             lineStyle
}

// canvas is a grid of cells that lines and text are drawn onto.
type canvas struct {
	w, h  int
	cells [][]cell
}

func newCanvas(w, h int) *canvas {
	c := &canvas{w: max(w, 0), h: max(h, 0)}
	c.cells = make([][]cell, c.h)
	for y := range c.cells {
		c.cells[y] = make([]cell, c.w)
	}
	return c
}

func (c *canvas) at(x, y int) *cell {
	if x < 0 || y < 0 || x >= c.w || y >= c.h {
		return nil
	}
	return &c.cells[y][x]
}

// edgeLine draws an orthogonal run from (x0,y0) to (x1,y1) for an edge.
func (c *canvas) edgeLine(x0, y0, x1, y1 int, st lineStyle) {
	c.line(x0, y0, x1, y1, func(cl *cell) {
		cl.round = true
		if !cl.box && st > cl.style {
			cl.style = st
		}
	})
}

// boxLine draws an orthogonal run that is part of a border.
func (c *canvas) boxLine(x0, y0, x1, y1 int, sharp bool) {
	c.line(x0, y0, x1, y1, func(cl *cell) {
		cl.box = true
		cl.style = styleSolid
		if sharp {
			cl.sharp = true
		} else {
			cl.round = true
		}
	})
}

// line adds the run's directions to every cell along it. Only horizontal
// and vertical runs are drawn; a diagonal one is ignored.
func (c *canvas) line(x0, y0, x1, y1 int, mark func(*cell)) {
	switch {
	case y0 == y1:
		if x0 > x1 {
			x0, x1 = x1, x0
		}
		for x := x0; x <= x1; x++ {
			cl := c.at(x, y0)
			if cl == nil {
				continue
			}
			if x > x0 {
				cl.mask |= dirLeft
			}
			if x < x1 {
				cl.mask |= dirRight
			}
			mark(cl)
		}
	case x0 == x1:
		if y0 > y1 {
			y0, y1 = y1, y0
		}
		for y := y0; y <= y1; y++ {
			cl := c.at(x0, y)
			if cl == nil {
				continue
			}
			if y > y0 {
				cl.mask |= dirUp
			}
			if y < y1 {
				cl.mask |= dirDown
			}
			mark(cl)
		}
	}
}

// rect draws a box border with its corners at (x0,y0) and (x1,y1).
func (c *canvas) rect(x0, y0, x1, y1 int, sharp bool) {
	c.boxLine(x0, y0, x1, y0, sharp)
	c.boxLine(x0, y1, x1, y1, sharp)
	c.boxLine(x0, y0, x0, y1, sharp)
	c.boxLine(x1, y0, x1, y1, sharp)
}

// clear blanks a rectangle, hiding whatever was drawn there.
func (c *canvas) clear(x0, y0, x1, y1 int) {
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			if cl := c.at(x, y); cl != nil {
				*cl = cell{}
			}
		}
	}
}

// put sets a one-cell glyph.
func (c *canvas) put(x, y int, g string) {
	if cl := c.at(x, y); cl != nil {
		cl.glyph, cl.cont = g, false
	}
}

// text writes s from (x,y) grapheme by grapheme, wide ones over two cells,
// and returns the cells it took. A wide grapheme that would straddle the
// right edge is dropped.
func (c *canvas) text(x, y int, s string) int {
	start := x
	g := uniseg.NewGraphemes(s)
	for g.Next() {
		cluster := g.Str()
		w := ansi.StringWidth(cluster)
		if w == 0 {
			continue
		}
		if x+w > c.w {
			break
		}
		if cl := c.at(x, y); cl != nil {
			cl.glyph, cl.cont = cluster, false
		}
		for i := 1; i < w; i++ {
			if cl := c.at(x+i, y); cl != nil {
				cl.glyph, cl.cont = "", true
			}
		}
		x += w
	}
	return x - start
}

// title writes " s " on a horizontal border from x, within w cells, at
// the first place where it hides no junction; it shortens s when needed and
// leaves the border bare when no place is clear.
func (c *canvas) title(x, y, w int, s, tail string) {
	plain := func(x0, x1 int) bool {
		for x := x0; x <= x1; x++ {
			cl := c.at(x, y)
			if cl == nil || cl.glyph != "" || cl.mask != dirLeft|dirRight {
				return false
			}
		}
		return true
	}
	for tw := min(width(s), w-2); tw >= 1; tw-- {
		t := " " + truncate(s, tw, tail) + " "
		n := width(t)
		for x0 := x; x0+n <= x+w; x0++ {
			if plain(x0, x0+n-1) {
				c.text(x0, y, t)
				return
			}
		}
	}
}

// lines renders the canvas, each row with its trailing blanks trimmed.
func (c *canvas) lines(ascii bool) []string {
	out := make([]string, c.h)
	var b strings.Builder
	for y, row := range c.cells {
		b.Reset()
		for x := 0; x < len(row); x++ {
			cl := &row[x]
			switch {
			case cl.cont:
				// The wide glyph before it covers this cell.
			case cl.glyph != "":
				b.WriteString(cl.glyph)
			case cl.mask != 0:
				b.WriteString(lineGlyph(cl, ascii))
			default:
				b.WriteByte(' ')
			}
		}
		out[y] = strings.TrimRight(b.String(), " ")
	}
	return out
}

// lineGlyph is the box-drawing character for a line cell.
func lineGlyph(cl *cell, ascii bool) string {
	m := cl.mask
	vertical := m&^(dirUp|dirDown) == 0
	horizontal := m&^(dirLeft|dirRight) == 0
	if ascii {
		switch {
		case vertical:
			if cl.style == styleDotted {
				return ":"
			}
			return "|"
		case horizontal:
			switch cl.style {
			case styleDotted:
				return "."
			case styleThick:
				return "="
			}
			return "-"
		}
		if cl.round && !cl.sharp && bits(m) == 2 {
			if m&dirDown != 0 {
				return "."
			}
			return "'"
		}
		return "+"
	}
	thick := cl.style == styleThick && !cl.box
	switch {
	case vertical:
		switch {
		case thick:
			return "┃"
		case cl.style == styleDotted && !cl.box:
			return "┆"
		}
		return "│"
	case horizontal:
		switch {
		case thick:
			return "━"
		case cl.style == styleDotted && !cl.box:
			return "┄"
		}
		return "─"
	}
	round := cl.round && !cl.sharp
	set := junctionLight
	if thick {
		set = junctionThick
		round = false
	}
	switch m {
	case dirDown | dirRight:
		if round {
			return "╭"
		}
	case dirDown | dirLeft:
		if round {
			return "╮"
		}
	case dirUp | dirRight:
		if round {
			return "╰"
		}
	case dirUp | dirLeft:
		if round {
			return "╯"
		}
	}
	if g, ok := set[m]; ok {
		return g
	}
	return "┼"
}

func bits(m uint8) int {
	n := 0
	for ; m != 0; m &= m - 1 {
		n++
	}
	return n
}

var junctionLight = map[uint8]string{
	dirDown | dirRight:                   "┌",
	dirDown | dirLeft:                    "┐",
	dirUp | dirRight:                     "└",
	dirUp | dirLeft:                      "┘",
	dirUp | dirDown | dirRight:           "├",
	dirUp | dirDown | dirLeft:            "┤",
	dirDown | dirLeft | dirRight:         "┬",
	dirUp | dirLeft | dirRight:           "┴",
	dirUp | dirDown | dirLeft | dirRight: "┼",
}

var junctionThick = map[uint8]string{
	dirDown | dirRight:                   "┏",
	dirDown | dirLeft:                    "┓",
	dirUp | dirRight:                     "┗",
	dirUp | dirLeft:                      "┛",
	dirUp | dirDown | dirRight:           "┣",
	dirUp | dirDown | dirLeft:            "┫",
	dirDown | dirLeft | dirRight:         "┳",
	dirUp | dirLeft | dirRight:           "┻",
	dirUp | dirDown | dirLeft | dirRight: "╋",
}

// width is s's display width in cells.
func width(s string) int { return ansi.StringWidth(s) }

// truncate cuts s to at most w cells, ending in tail when it had to cut.
func truncate(s string, w int, tail string) string {
	if width(s) <= w {
		return s
	}
	if w <= width(tail) {
		return cut(s, w)
	}
	return cut(s, w-width(tail)) + tail
}

// cut keeps the graphemes of s that fit in w cells.
func cut(s string, w int) string {
	var b strings.Builder
	used := 0
	g := uniseg.NewGraphemes(s)
	for g.Next() {
		cw := ansi.StringWidth(g.Str())
		if used+cw > w {
			break
		}
		b.WriteString(g.Str())
		used += cw
	}
	return b.String()
}

// wrapText breaks s into lines of at most w cells: at spaces where it can,
// anywhere inside a word (or a run of CJK text) longer than a line.
func wrapText(s string, w int) []string {
	if w < 1 || width(s) <= w {
		return []string{s}
	}
	var out []string
	line, lineW := "", 0
	flush := func() {
		out = append(out, strings.TrimRight(line, " "))
		line, lineW = "", 0
	}
	for _, word := range strings.Fields(s) {
		ww := width(word)
		switch {
		case lineW > 0 && lineW+1+ww <= w:
			line += " " + word
			lineW += 1 + ww
			continue
		case lineW > 0:
			flush()
		}
		for ww > w {
			head := cut(word, w)
			if head == "" {
				break
			}
			out = append(out, head)
			word = word[len(head):]
			ww = width(word)
		}
		line, lineW = word, ww
	}
	if lineW > 0 || len(out) == 0 {
		flush()
	}
	return out
}
