package mermaid

// Drawing turns the abstract layout into screen cells. Along the layer axis
// every layer takes the thickness of its thickest node, and the gap after
// it is made of, in order: a stub, the bottom borders of subgraphs that end
// at the layer, one track per bending edge, the edge labels, the top
// borders of subgraphs that start at the next layer and a row for the
// arrowheads.

// geometry holds the layer-axis coordinates of a laid out flowchart.
type geometry struct {
	start    []int // first cell of each layer
	thick    []int // thickness of each layer
	gapStart []int // first cell after each layer
	opens    []int // border rows for subgraphs starting at each layer
	closes   []int // border rows for subgraphs ending at each layer
	tracks   []int // bending tracks in the gap after each layer
	labels   []int // label rows (TD) or columns (LR) in that gap
	stub     int
	extent   int // layer-axis size of the drawing
	shift    int // order-axis offset that keeps every cell at x >= 0
	oExtent  int // order-axis size of the drawing
}

func (l *layout) geometry() *geometry {
	nl := len(l.layers)
	g := &geometry{
		start: make([]int, nl), thick: make([]int, nl), gapStart: make([]int, nl),
		opens: make([]int, nl), closes: make([]int, nl),
		tracks: make([]int, nl), labels: make([]int, nl), stub: 1,
	}
	if l.horiz {
		g.stub = 2
	}
	for i, ly := range l.layers {
		for _, v := range ly {
			g.thick[i] = max(g.thick[i], l.nodes[v].thick)
		}
	}
	for _, gr := range l.groups {
		if len(gr.members) == 0 {
			continue
		}
		g.opens[gr.minL] = max(g.opens[gr.minL], 2*gr.height+1)
		g.closes[gr.maxL] = max(g.closes[gr.maxL], 2*gr.height+1)
	}
	labelW := make([]int, nl)
	for _, s := range l.segs {
		gi := l.nodes[s.upper].layer
		if s.p != s.q {
			g.tracks[gi] = max(g.tracks[gi], s.track+1)
		}
		if s.label != "" {
			g.labels[gi] = max(g.labels[gi], s.level+1)
			labelW[gi] = max(labelW[gi], width(s.label))
		}
	}
	if l.horiz {
		for i, w := range labelW {
			g.labels[i] = 0
			if w > 0 {
				g.labels[i] = w + 2
			}
		}
	}
	a := 0
	if g.opens[0] > 0 {
		a = g.opens[0] + 1
	}
	for i := range nl {
		g.start[i] = a
		a += g.thick[i]
		g.gapStart[i] = a
		if i < nl-1 {
			a += g.stub + g.closes[i] + g.tracks[i] + g.labels[i] + g.opens[i+1] + 1
		}
	}
	if c := g.closes[nl-1]; c > 0 {
		a += g.stub + c
	}
	g.extent = a

	lo, hi := 0, 0
	for _, n := range l.nodes {
		hi = max(hi, n.end())
	}
	for _, gr := range l.groups {
		if len(gr.members) > 0 {
			lo, hi = min(lo, gr.x0), max(hi, gr.x1)
		}
	}
	if !l.horiz {
		for i := range l.segs {
			if s := &l.segs[i]; s.label != "" {
				a, b := l.labelSpan(s)
				lo, hi = min(lo, a), max(hi, b)
			}
		}
	}
	g.shift = -lo
	g.oExtent = hi - lo + 1
	return g
}

// track is the layer-axis cell of track t in the gap after layer i.
func (g *geometry) track(i, t int) int { return g.gapStart[i] + g.stub + g.closes[i] + t }

// labelBase is the first label row (TD) or column (LR) of a gap.
func (g *geometry) labelBase(i int) int { return g.track(i, g.tracks[i]) }

// pen draws in abstract coordinates: o along the order axis, a along the
// layer axis.
type pen struct {
	c   *canvas
	dir string
	g   *geometry
}

func (p *pen) pt(o, a int) (int, int) {
	o += p.g.shift
	switch p.dir {
	case "BT":
		return o, p.g.extent - 1 - a
	case "LR":
		return a, o
	case "RL":
		return p.g.extent - 1 - a, o
	}
	return o, a
}

func (p *pen) edge(o0, a0, o1, a1 int, st lineStyle) {
	x0, y0 := p.pt(o0, a0)
	x1, y1 := p.pt(o1, a1)
	p.c.edgeLine(x0, y0, x1, y1, st)
}

// rect maps an abstract box to its screen corners.
func (p *pen) rect(o0, a0, o1, a1 int) (int, int, int, int) {
	x0, y0 := p.pt(o0, a0)
	x1, y1 := p.pt(o1, a1)
	return min(x0, x1), min(y0, y1), max(x0, x1), max(y0, y1)
}

func (p *pen) put(o, a int, glyph string) {
	x, y := p.pt(o, a)
	p.c.put(x, y, glyph)
}

// text writes s along the screen row through (o,a); in LR and RL it spans
// the layer cells a .. a+width-1.
func (p *pen) text(o, a int, s string) {
	x, y := p.pt(o, a)
	if p.dir == "LR" || p.dir == "RL" {
		x2, _ := p.pt(o, a+width(s)-1)
		x = min(x, x2)
	}
	p.c.text(x, y, s)
}

// marker is the glyph for an edge end; fwd points along the layer axis.
func (l *layout) marker(kind string, fwd bool) string {
	switch kind {
	case "circle":
		return "o"
	case "cross":
		return "x"
	case "":
		return ""
	}
	var arrows [2]string // forward, back
	switch l.fc.dir {
	case "BT":
		arrows = [2]string{"▲", "▼"}
	case "LR":
		arrows = [2]string{"►", "◄"}
	case "RL":
		arrows = [2]string{"◄", "►"}
	default:
		arrows = [2]string{"▼", "▲"}
	}
	if l.ascii {
		for i, a := range arrows {
			arrows[i] = map[string]string{"▲": "^", "▼": "v", "►": ">", "◄": "<"}[a]
		}
	}
	if fwd {
		return arrows[0]
	}
	return arrows[1]
}

func (l *layout) draw() []string {
	g := l.geometry()
	p := &pen{dir: l.fc.dir, g: g}
	if l.horiz {
		p.c = newCanvas(g.extent, g.oExtent)
	} else {
		p.c = newCanvas(g.oExtent, g.extent)
	}

	// Subgraph boxes first, so edges and nodes draw over them.
	type title struct {
		x, y, w int
		s       string
	}
	var titles []title
	for _, gr := range l.groups {
		if len(gr.members) == 0 {
			continue
		}
		top := g.start[gr.minL] - 2 - 2*gr.height
		bottom := g.gapStart[gr.maxL] + g.stub + 2*gr.height
		x0, y0, x1, y1 := p.rect(gr.x0, top, gr.x1, bottom)
		p.c.rect(x0, y0, x1, y1, false)
		if gr.g.title != "" {
			titles = append(titles, title{x0 + 2, y0, x1 - x0 - 3, gr.g.title})
		}
	}

	var marks []func()
	for si, s := range l.segs {
		le := l.edges[s.edge]
		up, lo := l.nodes[s.upper], l.nodes[s.lower]
		gi := up.layer
		upMark, loMark := "", ""
		if le.segs[0] == si {
			upMark = le.e.tail
			if le.reversed {
				upMark = le.e.head
			}
		}
		if le.segs[len(le.segs)-1] == si {
			loMark = le.e.head
			if le.reversed {
				loMark = le.e.tail
			}
		}
		var a0, a1 int
		if up.dummy() {
			a0 = g.start[gi]
		} else {
			a0 = g.start[gi] + up.thick - 1
			if upMark != "" {
				a0++
				o, a, m := s.p, a0, l.marker(upMark, false)
				marks = append(marks, func() { p.put(o, a, m) })
			}
		}
		if lo.dummy() {
			a1 = g.start[gi+1] + g.thick[gi+1] - 1
		} else {
			a1 = g.start[gi+1]
			if loMark != "" {
				a1--
				o, a, m := s.q, a1, l.marker(loMark, true)
				marks = append(marks, func() { p.put(o, a, m) })
			}
		}
		st := le.e.style
		if s.p == s.q {
			p.edge(s.p, a0, s.q, a1, st)
		} else {
			t := g.track(gi, s.track)
			p.edge(s.p, a0, s.p, t, st)
			p.edge(s.p, t, s.q, t, st)
			p.edge(s.q, t, s.q, a1, st)
		}
		if s.label != "" {
			o, a, lb := 0, 0, s.label
			if l.horiz {
				o, a = s.q, g.labelBase(gi)+1
			} else {
				o, _ = l.labelSpan(&l.segs[si])
				a = g.labelBase(gi) + s.level
			}
			marks = append(marks, func() { p.text(o, a, lb) })
		}
	}

	for _, n := range l.nodes {
		if n.dummy() {
			continue
		}
		a := g.start[n.layer]
		x0, y0, x1, y1 := p.rect(n.pos, a, n.end(), a+n.thick-1)
		p.c.rect(x0, y0, x1, y1, n.shape == shapeRect)
		if n.shape == shapeDiamond {
			tl, tr := "╱", "╲"
			if l.ascii {
				tl, tr = "/", "\\"
			}
			p.c.put(x0, y0, tl)
			p.c.put(x1, y0, tr)
			p.c.put(x0, y1, tr)
			p.c.put(x1, y1, tl)
		}
		w, h := x1-x0+1, y1-y0+1
		top := y0 + (h-len(n.lines))/2
		for i, s := range n.lines {
			p.c.text(x0+(w-width(s))/2, top+i, s)
		}
	}
	for _, m := range marks {
		m()
	}
	for _, t := range titles {
		p.c.title(t.x, t.y, t.w, t.s, l.ellipsis())
	}
	return p.c.lines(l.ascii)
}
