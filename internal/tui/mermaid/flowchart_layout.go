package mermaid

import (
	"math"
	"slices"
	"sort"
	"strings"
)

// The layout works in two abstract axes: the layer axis, along which edges
// run from layer to layer (down for TD, right for LR), and the order axis,
// along which the nodes of one layer sit side by side. Drawing maps them to
// screen columns and rows for the diagram's direction.

// lnode is a node of the layered graph: a flowchart node or a dummy that
// carries a long edge through a layer.
type lnode struct {
	node  int // flowchart node, -1 for a dummy
	layer int
	idx   int     // position within its layer
	val   float64 // sort value of the ordering sweeps
	pos   int     // order-axis start
	size  int     // order-axis extent
	thick int     // layer-axis extent
	lines []string
	shape shape
	// chain is the subgraphs it sits in, outermost first.
	chain []int
	up    []int // lnodes in the layer above joined by a segment
	down  []int
}

func (n *lnode) dummy() bool        { return n.node < 0 }
func (n *lnode) center() float64    { return float64(n.pos) + float64(n.size-1)/2 }
func (n *lnode) end() int           { return n.pos + n.size - 1 }
func (n *lnode) inGroup(g int) bool { return slices.Contains(n.chain, g) }

// lseg is the part of an edge between two adjacent layers.
type lseg struct {
	edge         int
	upper, lower int
	p, q         int // order-axis ports on the upper and lower node
	track        int
	level        int // label row in TD, unused in LR
	labelLo      int // first order-axis cell of a TD label
	label        string
}

// ledge is an edge of the layered graph.
type ledge struct {
	e        fcEdge
	reversed bool
	segs     []int
}

// lgroup is a subgraph's box.
type lgroup struct {
	g        fcGroup
	depth    int // 1 for a top-level subgraph
	height   int // nesting levels below it
	minL     int
	maxL     int
	x0, x1   int
	members  []int
	hasNodes bool
}

type layout struct {
	fc      *flowchart
	ascii   bool
	horiz   bool // LR / RL: the layer axis is horizontal
	nodes   []*lnode
	layers  [][]int
	edges   []ledge
	segs    []lseg
	groups  []*lgroup
	depth   int // deepest subgraph nesting, 0 without subgraphs
	noGroup bool
}

const maxEdgeLabel = 24

func renderFlowchart(lines []srcLine, opt Options) (*Diagram, error) {
	fc, err := parseFlowchart(lines)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, wrap := range []int{32, 20, 12} {
		out = layoutFlowchart(fc, wrap, opt.ASCII)
		if opt.Width <= 0 || maxWidth(out) <= opt.Width {
			break
		}
	}
	return &Diagram{Kind: "flowchart", Lines: out}, nil
}

func maxWidth(lines []string) int {
	w := 0
	for _, l := range lines {
		w = max(w, width(l))
	}
	return w
}

func layoutFlowchart(fc *flowchart, wrap int, ascii bool) []string {
	l := &layout{fc: fc, ascii: ascii, horiz: fc.dir == "LR" || fc.dir == "RL"}
	l.build(wrap)
	if !l.place() {
		// The boxes could not be kept apart: lay out again without them.
		l = &layout{fc: fc, ascii: ascii, horiz: l.horiz, noGroup: true}
		l.build(wrap)
		l.place()
	}
	return l.draw()
}

// build makes the layered graph: node sizes, cycle breaking, layers,
// dummies and the group chains.
func (l *layout) build(wrap int) {
	fc := l.fc
	chains := make([][]int, len(fc.nodes))
	if !l.noGroup {
		for i, n := range fc.nodes {
			for g := n.group; g >= 0; g = fc.groups[g].parent {
				chains[i] = append([]int{g}, chains[i]...)
			}
		}
	}
	for i, n := range fc.nodes {
		ln := &lnode{node: i, shape: n.shape, chain: chains[i]}
		ln.lines = l.nodeLines(n, wrap)
		l.nodes = append(l.nodes, ln)
	}

	// Break cycles: a depth-first search in source order reverses every
	// edge that closes a cycle.
	out := make([][]int, len(fc.nodes))
	for i, e := range fc.edges {
		out[e.from] = append(out[e.from], i)
	}
	state := make([]uint8, len(fc.nodes)) // 0 new, 1 on stack, 2 done
	reversed := make([]bool, len(fc.edges))
	var dfs func(int)
	dfs = func(v int) {
		state[v] = 1
		for _, ei := range out[v] {
			w := fc.edges[ei].to
			switch state[w] {
			case 0:
				dfs(w)
			case 1:
				reversed[ei] = true
			}
		}
		state[v] = 2
	}
	for v := range fc.nodes {
		if state[v] == 0 {
			dfs(v)
		}
	}

	// Longest-path layers over the acyclic edges, then sources move down
	// next to their first successor.
	preds := make([][]int, len(fc.nodes))
	succs := make([][]int, len(fc.nodes))
	for i, e := range fc.edges {
		a, b := e.from, e.to
		if reversed[i] {
			a, b = b, a
		}
		succs[a] = append(succs[a], b)
		preds[b] = append(preds[b], a)
	}
	order := topoOrder(len(fc.nodes), preds, succs)
	layer := make([]int, len(fc.nodes))
	for _, v := range order {
		for _, u := range preds[v] {
			layer[v] = max(layer[v], layer[u]+1)
		}
	}
	for i := len(order) - 1; i >= 0; i-- {
		v := order[i]
		if len(preds[v]) > 0 || len(succs[v]) == 0 {
			continue
		}
		lo := math.MaxInt
		for _, w := range succs[v] {
			lo = min(lo, layer[w]-1)
		}
		layer[v] = max(layer[v], lo)
	}
	nl := 0
	for v := range fc.nodes {
		l.nodes[v].layer = layer[v]
		nl = max(nl, layer[v]+1)
	}
	l.layers = make([][]int, nl)
	for v := range fc.nodes {
		l.layers[layer[v]] = append(l.layers[layer[v]], v)
	}

	// Split edges into one segment per layer gap, adding dummies.
	for i, e := range fc.edges {
		a, b := e.from, e.to
		if reversed[i] {
			a, b = b, a
		}
		le := ledge{e: e, reversed: reversed[i]}
		chain := commonPrefix(l.nodes[a].chain, l.nodes[b].chain)
		prev := a
		for ly := layer[a] + 1; ly <= layer[b]; ly++ {
			next := b
			if ly < layer[b] {
				next = len(l.nodes)
				l.nodes = append(l.nodes, &lnode{node: -1, layer: ly, size: 1, chain: chain})
				l.layers[ly] = append(l.layers[ly], next)
			}
			le.segs = append(le.segs, len(l.segs))
			l.segs = append(l.segs, lseg{edge: i, upper: prev, lower: next})
			l.nodes[prev].down = append(l.nodes[prev].down, next)
			l.nodes[next].up = append(l.nodes[next].up, prev)
			prev = next
		}
		if label := e.label; label != "" {
			last := &l.segs[le.segs[len(le.segs)-1]]
			last.label = truncate(strings.ReplaceAll(label, "\n", " "), maxEdgeLabel, l.ellipsis())
		}
		l.edges = append(l.edges, le)
	}

	// Sizes: text plus border and padding, wide enough for every port.
	for _, n := range l.nodes {
		ports := max(len(n.up), len(n.down))
		if n.dummy() {
			continue
		}
		tw := 0
		for _, s := range n.lines {
			tw = max(tw, width(s))
		}
		w, h := tw+4, len(n.lines)+2
		if l.horiz {
			n.size, n.thick = max(h, 2*ports+1), w
		} else {
			n.size, n.thick = max(w, 2*ports+1), h
		}
	}
	for _, ly := range l.layers {
		t := 1
		for _, v := range ly {
			t = max(t, l.nodes[v].thick)
		}
		for _, v := range ly {
			if l.nodes[v].dummy() {
				l.nodes[v].thick = t
			}
		}
	}
	if !l.noGroup {
		l.buildGroups()
	}
}

func (l *layout) ellipsis() string {
	if l.ascii {
		return "~"
	}
	return "…"
}

// nodeLines is a node's text: its label lines wrapped to wrap cells, and a
// mark for each edge back to itself.
func (l *layout) nodeLines(n *fcNode, wrap int) []string {
	var out []string
	for s := range strings.SplitSeq(n.label, "\n") {
		out = append(out, wrapText(s, wrap)...)
	}
	loop := "↺"
	if l.ascii {
		loop = "(loop)"
	}
	for _, lb := range n.loops {
		if lb == "" {
			out[len(out)-1] += " " + loop
			continue
		}
		out = append(out, wrapText(loop+" "+strings.ReplaceAll(lb, "\n", " "), wrap)...)
	}
	return out
}

// topoOrder is a topological order of the acyclic graph, taking ready
// nodes in source order.
func topoOrder(n int, preds, succs [][]int) []int {
	indeg := make([]int, n)
	for v := range n {
		indeg[v] = len(preds[v])
	}
	var ready, out []int
	for v := range n {
		if indeg[v] == 0 {
			ready = append(ready, v)
		}
	}
	for len(ready) > 0 {
		sort.Ints(ready)
		v := ready[0]
		ready = ready[1:]
		out = append(out, v)
		for _, w := range succs[v] {
			if indeg[w]--; indeg[w] == 0 {
				ready = append(ready, w)
			}
		}
	}
	return out
}

func commonPrefix(a, b []int) []int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return a[:n:n]
}

func (l *layout) buildGroups() {
	fc := l.fc
	for i, g := range fc.groups {
		lg := &lgroup{g: g, minL: math.MaxInt, maxL: -1}
		for p := i; p >= 0; p = fc.groups[p].parent {
			lg.depth++
		}
		l.depth = max(l.depth, lg.depth)
		l.groups = append(l.groups, lg)
	}
	for i := range fc.groups {
		for p := fc.groups[i].parent; p >= 0; p = fc.groups[p].parent {
			l.groups[p].height = max(l.groups[p].height, l.groups[i].depth-l.groups[p].depth)
		}
	}
	for v, n := range l.nodes {
		for _, g := range n.chain {
			lg := l.groups[g]
			lg.members = append(lg.members, v)
			lg.minL, lg.maxL = min(lg.minL, n.layer), max(lg.maxL, n.layer)
			if !n.dummy() {
				lg.hasNodes = true
			}
		}
	}
}

// gapBetween is the least order-axis room between two neighbours in a
// layer: more when subgraph borders run between them.
func (l *layout) gapBetween(a, b *lnode) int {
	base := 2
	if l.horiz {
		base = 1
	}
	if a.dummy() && b.dummy() {
		base = 1
	}
	k := len(a.chain) + len(b.chain) - 2*len(commonPrefix(a.chain, b.chain))
	return max(base, 2*k+1)
}

// place orders the layers and positions every node. It reports false when
// subgraph boxes could not be kept apart.
func (l *layout) place() bool {
	l.order()
	l.position()
	if len(l.groups) > 0 && !l.separateGroups() {
		return false
	}
	l.assignPorts()
	l.assignTracks()
	return true
}

// order runs barycentre sweeps, keeping the ordering with fewest crossings.
func (l *layout) order() {
	for _, ly := range l.layers {
		for i, v := range ly {
			l.nodes[v].idx = i
			l.nodes[v].val = float64(i)
		}
	}
	best := l.snapshot()
	bestX := l.crossings()
	for it := range 12 {
		down := it%2 == 0
		for k := range l.layers {
			li := k
			if !down {
				li = len(l.layers) - 1 - k
			}
			l.sortLayer(li, down)
		}
		if x := l.crossings(); x < bestX {
			best, bestX = l.snapshot(), x
		}
	}
	l.restore(best)
}

func (l *layout) snapshot() [][]int {
	out := make([][]int, len(l.layers))
	for i, ly := range l.layers {
		out[i] = slices.Clone(ly)
	}
	return out
}

func (l *layout) restore(s [][]int) {
	l.layers = s
	for _, ly := range l.layers {
		for i, v := range ly {
			l.nodes[v].idx = i
		}
	}
}

// sortLayer sorts one layer by the mean position of its neighbours above
// (down sweep) or below, keeping each subgraph's members together.
func (l *layout) sortLayer(li int, down bool) {
	ly := l.layers[li]
	for _, v := range ly {
		n := l.nodes[v]
		nb := n.up
		if !down {
			nb = n.down
		}
		if len(nb) == 0 {
			n.val = float64(n.idx)
			continue
		}
		sum := 0.0
		for _, u := range nb {
			sum += float64(l.nodes[u].idx)
		}
		n.val = sum / float64(len(nb))
	}
	groupVal := map[int]float64{}
	if len(l.groups) > 0 {
		cnt := map[int]int{}
		for _, v := range ly {
			for _, g := range l.nodes[v].chain {
				groupVal[g] += l.nodes[v].val
				cnt[g]++
			}
		}
		for g := range groupVal {
			groupVal[g] /= float64(cnt[g])
		}
	}
	item := func(n *lnode, depth int) (float64, int) {
		if depth < len(n.chain) {
			return groupVal[n.chain[depth]], -1 - n.chain[depth]
		}
		return n.val, n.idx
	}
	sort.SliceStable(ly, func(i, j int) bool {
		a, b := l.nodes[ly[i]], l.nodes[ly[j]]
		d := len(commonPrefix(a.chain, b.chain))
		va, ta := item(a, d)
		vb, tb := item(b, d)
		if va != vb {
			return va < vb
		}
		if ta != tb {
			return ta < tb
		}
		return a.idx < b.idx
	})
	for i, v := range ly {
		l.nodes[v].idx = i
	}
}

// crossings counts segment crossings between adjacent layers.
func (l *layout) crossings() int {
	byLayer := make([][]lseg, len(l.layers))
	for _, s := range l.segs {
		ly := l.nodes[s.upper].layer
		byLayer[ly] = append(byLayer[ly], s)
	}
	n := 0
	for _, segs := range byLayer {
		for i := range segs {
			for j := i + 1; j < len(segs); j++ {
				a1, b1 := l.nodes[segs[i].upper].idx, l.nodes[segs[i].lower].idx
				a2, b2 := l.nodes[segs[j].upper].idx, l.nodes[segs[j].lower].idx
				if (a1-a2)*(b1-b2) < 0 {
					n++
				}
			}
		}
	}
	return n
}

// position packs each layer, then centres nodes over their neighbours with
// alternating sweeps, ending on a downward one.
func (l *layout) position() {
	for _, ly := range l.layers {
		x := 0
		for i, v := range ly {
			n := l.nodes[v]
			if i > 0 {
				x += l.gapBetween(l.nodes[ly[i-1]], n)
			}
			n.pos = x
			x += n.size
		}
	}
	for _, down := range []bool{true, false, true, false, true} {
		for k := range l.layers {
			li := k
			if !down {
				li = len(l.layers) - 1 - k
			}
			l.balance(li, down)
		}
	}
	l.normalize()
}

// balance moves a layer's nodes as close as their order allows to the mean
// centre of their neighbours (pool adjacent violators on the offsets).
func (l *layout) balance(li int, down bool) {
	ly := l.layers[li]
	if len(ly) == 0 {
		return
	}
	off := make([]float64, len(ly))
	want := make([]float64, len(ly))
	acc := 0
	for i, v := range ly {
		n := l.nodes[v]
		if i > 0 {
			acc += l.nodes[ly[i-1]].size + l.gapBetween(l.nodes[ly[i-1]], n)
		}
		off[i] = float64(acc)
		nb := n.up
		if !down {
			nb = n.down
		}
		c := n.center()
		if len(nb) > 0 {
			sum := 0.0
			for _, u := range nb {
				sum += l.nodes[u].center()
			}
			c = sum / float64(len(nb))
		}
		want[i] = c - float64(n.size-1)/2 - off[i]
	}
	type block struct {
		sum   float64
		count int
	}
	var blocks []block
	var owner []int
	for i := range ly {
		blocks = append(blocks, block{want[i], 1})
		for len(blocks) > 1 {
			a, b := blocks[len(blocks)-2], blocks[len(blocks)-1]
			if a.sum/float64(a.count) <= b.sum/float64(b.count) {
				break
			}
			blocks = blocks[:len(blocks)-2]
			blocks = append(blocks, block{a.sum + b.sum, a.count + b.count})
		}
	}
	for bi, b := range blocks {
		for range b.count {
			owner = append(owner, bi)
		}
	}
	for i, v := range ly {
		b := blocks[owner[i]]
		y := b.sum / float64(b.count)
		l.nodes[v].pos = int(math.Floor(y+0.5)) + int(off[i])
	}
}

// normalize shifts every node so the leftmost starts at zero.
func (l *layout) normalize() {
	lo := math.MaxInt
	for _, n := range l.nodes {
		lo = min(lo, n.pos)
	}
	for _, n := range l.nodes {
		n.pos -= lo
	}
}

// pushRight restores the packing of layer li from index i on.
func (l *layout) pushRight(li, i int) bool {
	ly := l.layers[li]
	moved := false
	for j := max(i, 1); j < len(ly); j++ {
		a, b := l.nodes[ly[j-1]], l.nodes[ly[j]]
		if lo := a.end() + 1 + l.gapBetween(a, b); b.pos < lo {
			b.pos = lo
			moved = true
		}
	}
	return moved
}

// groupExtent recomputes a box's order-axis span from its members.
func (l *layout) groupExtent(g *lgroup) {
	lo, hi := math.MaxInt, math.MinInt
	for _, v := range g.members {
		lo, hi = min(lo, l.nodes[v].pos), max(hi, l.nodes[v].end())
	}
	m := 2 + 2*g.height
	g.x0, g.x1 = lo-m, hi+m
	if !l.horiz {
		if need := width(g.g.title) + 6; g.x1-g.x0+1 < need {
			g.x1 = g.x0 + need - 1
		}
	}
}

// side reports whether node n sits left of group g: by the layer order
// where g has members in n's layer, else by centres.
func (l *layout) leftOf(n *lnode, g *lgroup) bool {
	for _, v := range l.layers[n.layer] {
		if l.nodes[v].inGroup(slices.Index(l.groups, g)) {
			return n.idx < l.nodes[v].idx
		}
	}
	return n.center() < float64(g.x0+g.x1)/2
}

// separateGroups pushes nodes and boxes right until no box overlaps a node
// outside it or another box. It reports false if that does not settle.
func (l *layout) separateGroups() bool {
	shiftGroup := func(g *lgroup, by int) {
		touched := map[int]int{}
		for _, v := range g.members {
			n := l.nodes[v]
			n.pos += by
			if i, ok := touched[n.layer]; !ok || n.idx > i {
				touched[n.layer] = n.idx
			}
		}
		for li, i := range touched {
			l.pushRight(li, i)
		}
	}
	for range 200 {
		changed := false
		for _, g := range l.groups {
			l.groupExtent(g)
		}
		for gi, g := range l.groups {
			if len(g.members) == 0 {
				continue
			}
			for li := g.minL; li <= g.maxL && !changed; li++ {
				for _, v := range l.layers[li] {
					n := l.nodes[v]
					if n.inGroup(gi) {
						continue
					}
					if l.leftOf(n, g) {
						if d := n.end() + 2 - g.x0; d > 0 {
							shiftGroup(g, d)
							changed = true
							break
						}
					} else if d := g.x1 + 2 - n.pos; d > 0 {
						n.pos += d
						l.pushRight(li, n.idx)
						changed = true
						break
					}
				}
			}
			if changed {
				break
			}
			for hi, h := range l.groups {
				if hi == gi || len(h.members) == 0 || h.maxL < g.minL || h.minL > g.maxL || l.nested(gi, hi) {
					continue
				}
				if g.x0 <= h.x0 && h.x0 < g.x1+2 {
					shiftGroup(h, g.x1+2-h.x0)
					changed = true
					break
				}
			}
			if changed {
				break
			}
		}
		if !changed {
			for _, g := range l.groups {
				l.groupExtent(g)
			}
			lo := 0
			for _, g := range l.groups {
				if len(g.members) > 0 {
					lo = min(lo, g.x0)
				}
			}
			if lo < 0 {
				for _, n := range l.nodes {
					n.pos -= lo
				}
				for _, g := range l.groups {
					g.x0 -= lo
					g.x1 -= lo
				}
			}
			return true
		}
	}
	return false
}

// nested reports whether one group contains the other.
func (l *layout) nested(a, b int) bool {
	for p := l.fc.groups[a].parent; p >= 0; p = l.fc.groups[p].parent {
		if p == b {
			return true
		}
	}
	for p := l.fc.groups[b].parent; p >= 0; p = l.fc.groups[p].parent {
		if p == a {
			return true
		}
	}
	return false
}

// assignPorts spreads each node's segments along its border, ordered by the
// position of the node at the other end so they do not cross.
func (l *layout) assignPorts() {
	outs := make([][]int, len(l.nodes))
	ins := make([][]int, len(l.nodes))
	for i, s := range l.segs {
		outs[s.upper] = append(outs[s.upper], i)
		ins[s.lower] = append(ins[s.lower], i)
	}
	spread := func(n *lnode, segs []int, other func(lseg) int, set func(*lseg, int)) {
		sort.SliceStable(segs, func(a, b int) bool {
			return l.nodes[other(l.segs[segs[a]])].center() < l.nodes[other(l.segs[segs[b]])].center()
		})
		k := len(segs)
		if k == 0 {
			return
		}
		if n.dummy() {
			for _, s := range segs {
				set(&l.segs[s], n.pos)
			}
			return
		}
		inner := n.size - 2
		start := n.pos + 1 + (inner-(2*k-1))/2
		for i, s := range segs {
			set(&l.segs[s], start+2*i)
		}
	}
	for v, n := range l.nodes {
		spread(n, outs[v], func(s lseg) int { return s.lower }, func(s *lseg, x int) { s.p = x })
		spread(n, ins[v], func(s lseg) int { return s.upper }, func(s *lseg, x int) { s.q = x })
	}
	// A lone segment between two nodes that overlap runs straight: one
	// port moves over the other when it stays inside its node.
	inside := func(n *lnode, x int) bool {
		if n.dummy() {
			return x == n.pos
		}
		return x > n.pos && x < n.end()
	}
	for i := range l.segs {
		s := &l.segs[i]
		if s.p == s.q || len(outs[s.upper]) != 1 || len(ins[s.lower]) != 1 {
			continue
		}
		switch {
		case inside(l.nodes[s.upper], s.q):
			s.p = s.q
		case inside(l.nodes[s.lower], s.p):
			s.q = s.p
		}
	}
}

// assignTracks gives each bending segment a row (TD) or column (LR) of its
// gap, ordered so that segments leaving side by side do not cross, and
// stacks TD labels into rows where they would overlap.
func (l *layout) assignTracks() {
	byGap := make([][]int, len(l.layers))
	for i, s := range l.segs {
		ly := l.nodes[s.upper].layer
		byGap[ly] = append(byGap[ly], i)
	}
	for _, segs := range byGap {
		var bend []int
		for _, i := range segs {
			if l.segs[i].p != l.segs[i].q {
				bend = append(bend, i)
			}
		}
		sort.SliceStable(bend, func(a, b int) bool {
			sa, sb := l.segs[bend[a]], l.segs[bend[b]]
			la, lb := sa.p > sa.q, sb.p > sb.q
			if la != lb {
				return la
			}
			if la {
				return sa.p < sb.p
			}
			return sa.p > sb.p
		})
		for i, a := range bend {
			sa := &l.segs[a]
			sa.track = 0
			for _, b := range bend[:i] {
				sb := l.segs[b]
				if overlaps(sa.p, sa.q, sb.p, sb.q) {
					sa.track = max(sa.track, sb.track+1)
				}
			}
		}
		if l.horiz {
			continue
		}
		l.placeLabels(segs)
		var labelled []int
		for _, i := range segs {
			if l.segs[i].label != "" {
				labelled = append(labelled, i)
			}
		}
		sort.SliceStable(labelled, func(a, b int) bool { return l.segs[labelled[a]].q < l.segs[labelled[b]].q })
		var ends []int
		for _, i := range labelled {
			s := &l.segs[i]
			lo, _ := l.labelSpan(s)
			s.level = len(ends)
			for lv, e := range ends {
				if e+1 < lo {
					s.level = lv
					break
				}
			}
			_, hi := l.labelSpan(s)
			if s.level == len(ends) {
				ends = append(ends, hi)
			} else {
				ends[s.level] = hi
			}
		}
	}
}

func overlaps(a0, a1, b0, b1 int) bool {
	return min(a0, a1) <= max(b0, b1) && min(b0, b1) <= max(a0, a1)
}

// labelSpan is a TD label's order-axis span, beside the edge's last
// vertical run.
func (l *layout) labelSpan(s *lseg) (int, int) {
	return s.labelLo, s.labelLo + width(s.label) - 1
}

// placeLabels puts each TD label of a gap beside its edge: right of it, or
// left when the right would cover another edge's line.
func (l *layout) placeLabels(segs []int) {
	var xs []int
	for _, i := range segs {
		xs = append(xs, l.segs[i].q)
	}
	clear := func(own, lo, hi int) bool {
		for _, x := range xs {
			if x != own && x >= lo-1 && x <= hi+1 {
				return false
			}
		}
		return true
	}
	for _, i := range segs {
		s := &l.segs[i]
		if s.label == "" {
			continue
		}
		w := width(s.label)
		s.labelLo = s.q + 2
		if !clear(s.q, s.q+2, s.q+1+w) && clear(s.q, s.q-1-w, s.q-2) {
			s.labelLo = s.q - 1 - w
		}
	}
}
