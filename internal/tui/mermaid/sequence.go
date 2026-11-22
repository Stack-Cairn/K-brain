package mermaid

import (
	"regexp"
	"strconv"
	"strings"
)

// A sequence diagram is drawn as a row of participant boxes with a lifeline
// under each; every event below takes its own rows, top to bottom in source
// order. Columns are spaced so each message label fits between its two
// lifelines and each note fits beside its participant.

type seqParticipant struct {
	id, label string
	actor     bool
	w         int // box width
}

type seqEventKind uint8

const (
	evMessage seqEventKind = iota
	evNote
	evFrameStart
	evFrameElse
	evFrameEnd
)

type seqEvent struct {
	kind     seqEventKind
	from, to int // participants; for a note the span it covers
	text     string
	style    lineStyle
	head     string // "arrow", "cross" or ""
	both     bool   // arrowheads at both ends
	side     string // note: "left", "right" or "over"
	frame    int    // frame index for frame events
	lines    []string
}

type seqFrame struct {
	kind, label      string
	parent           int
	start, end       int // event indexes
	x0, x1           int
	startRow, endRow int
	elseRows         []int
	elseLabels       []string
}

type sequence struct {
	parts  []*seqParticipant
	byID   map[string]int
	events []seqEvent
	frames []*seqFrame
	number bool
}

var (
	seqParticipantRE = regexp.MustCompile(`^(?:create\s+)?(participant|actor)\s+(.+?)(?:\s+as\s+(.+))?$`)
	seqMessageRE     = regexp.MustCompile(`^(.+?)\s*(<<-->>|<<->>|-->>|->>|--x|-x|--\)|-\)|-->|->)\s*([+-]?)\s*([^:]+?)\s*(?::(.*))?$`)
	seqNoteRE        = regexp.MustCompile(`(?i)^note\s+(left of|right of|over)\s+([^:]+?)\s*:(.*)$`)
	seqFrameRE       = regexp.MustCompile(`^(loop|alt|opt|par|critical|break|rect|else|and|option)\b\s*(.*)$`)
	seqIgnoredRE     = regexp.MustCompile(`^(?:activate|deactivate|destroy|title|accTitle|accDescr|link|links|properties|details)\b`)
)

func parseSequence(lines []srcLine) (*sequence, error) {
	sq := &sequence{byID: map[string]int{}}
	var stack []int // open frames; -1 for a box block
	for _, l := range lines[1:] {
		t := strings.TrimSuffix(l.text, ";")
		switch {
		case t == "autonumber" || strings.HasPrefix(t, "autonumber "):
			sq.number = true
		case t == "end":
			if len(stack) == 0 {
				return nil, &ParseError{Line: l.n, Msg: "end without a block"}
			}
			top := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if top >= 0 {
				sq.frames[top].end = len(sq.events)
				sq.events = append(sq.events, seqEvent{kind: evFrameEnd, frame: top})
			}
		case strings.HasPrefix(t, "box ") || t == "box":
			stack = append(stack, -1)
		case seqIgnoredRE.MatchString(t):
		default:
			if err := sq.statement(t, l.n, &stack); err != nil {
				return nil, err
			}
		}
		if len(sq.events) > maxEvents || len(sq.parts) > maxNodes {
			return nil, ErrTooLarge
		}
	}
	for _, f := range stack {
		if f >= 0 {
			return nil, &ParseError{Line: lines[len(lines)-1].n, Msg: "unclosed " + sq.frames[f].kind}
		}
	}
	if len(sq.parts) == 0 {
		return nil, &ParseError{Line: lines[0].n, Msg: "no participants"}
	}
	return sq, nil
}

func (sq *sequence) participant(id string) int {
	id = strings.TrimSpace(id)
	if i, ok := sq.byID[id]; ok {
		return i
	}
	sq.byID[id] = len(sq.parts)
	sq.parts = append(sq.parts, &seqParticipant{id: id, label: id})
	return len(sq.parts) - 1
}

func (sq *sequence) statement(t string, line int, stack *[]int) error {
	if m := seqParticipantRE.FindStringSubmatch(t); m != nil {
		id := unquote(strings.TrimSpace(m[2]))
		p := sq.parts[sq.participant(id)]
		p.actor = m[1] == "actor"
		if m[3] != "" {
			p.label = strings.ReplaceAll(cleanLabel(m[3]), "\n", " ")
		}
		return nil
	}
	if m := seqNoteRE.FindStringSubmatch(t); m != nil {
		ids := strings.Split(m[2], ",")
		a := sq.participant(ids[0])
		b := a
		if len(ids) > 1 {
			b = sq.participant(ids[1])
		}
		if a > b {
			a, b = b, a
		}
		side := strings.Fields(strings.ToLower(m[1]))[0]
		sq.events = append(sq.events, seqEvent{kind: evNote, from: a, to: b, side: side, text: cleanLabel(m[3])})
		return nil
	}
	if m := seqFrameRE.FindStringSubmatch(t); m != nil {
		kind, label := m[1], cleanLabel(m[2])
		switch kind {
		case "else", "and", "option":
			if len(*stack) == 0 || (*stack)[len(*stack)-1] < 0 {
				return &ParseError{Line: line, Msg: kind + " outside a block"}
			}
			f := (*stack)[len(*stack)-1]
			sq.events = append(sq.events, seqEvent{kind: evFrameElse, frame: f, text: label})
			return nil
		case "rect":
			label = ""
		}
		parent := -1
		for i := len(*stack) - 1; i >= 0; i-- {
			if (*stack)[i] >= 0 {
				parent = (*stack)[i]
				break
			}
		}
		sq.frames = append(sq.frames, &seqFrame{kind: kind, label: label, parent: parent, start: len(sq.events)})
		f := len(sq.frames) - 1
		*stack = append(*stack, f)
		sq.events = append(sq.events, seqEvent{kind: evFrameStart, frame: f})
		return nil
	}
	if m := seqMessageRE.FindStringSubmatch(t); m != nil {
		e := seqEvent{kind: evMessage, from: sq.participant(m[1]), to: sq.participant(m[4]), text: cleanLabel(m[5])}
		arrow := m[2]
		if strings.HasPrefix(arrow, "<<") {
			e.both = true
		}
		if strings.Count(arrow, "-") >= 2 {
			e.style = styleDotted
		}
		switch {
		case strings.HasSuffix(arrow, "x"):
			e.head = "cross"
		case strings.HasSuffix(arrow, ">>"), strings.HasSuffix(arrow, ")"):
			e.head = "arrow"
		}
		sq.events = append(sq.events, e)
		return nil
	}
	return &ParseError{Line: line, Msg: "unknown statement: " + truncate(t, 40, "…")}
}

func renderSequence(lines []srcLine, opt Options) (*Diagram, error) {
	sq, err := parseSequence(lines)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, wrap := range []int{40, 24, 14} {
		out = sq.draw(wrap, opt.ASCII)
		if opt.Width <= 0 || maxWidth(out) <= opt.Width {
			break
		}
	}
	return &Diagram{Kind: "sequence", Lines: out}, nil
}

// wrapLines wraps every line of s to w cells.
func wrapLines(s string, w int) []string {
	var out []string
	for l := range strings.SplitSeq(s, "\n") {
		out = append(out, wrapText(l, w)...)
	}
	return out
}

// draw lays the diagram out with labels wrapped to wrap cells.
func (sq *sequence) draw(wrap int, ascii bool) []string {
	n := len(sq.parts)
	for _, p := range sq.parts {
		p.w = width(p.label) + 4
	}
	num := 0
	for i := range sq.events {
		e := &sq.events[i]
		text := e.text
		if e.kind == evMessage && sq.number {
			num++
			text = strconv.Itoa(num) + ". " + text
		}
		e.lines = nil
		if e.kind == evMessage || e.kind == evNote {
			if strings.TrimSpace(text) != "" {
				e.lines = wrapLines(text, wrap)
			}
		}
	}

	// Column spacing.
	gap := make([]int, max(n-1, 0))
	half := func(i int) (int, int) { return (sq.parts[i].w - 1) / 2, sq.parts[i].w / 2 }
	for i := range gap {
		_, r := half(i)
		l, _ := half(i + 1)
		gap[i] = r + l + 3
	}
	leftExtra, rightExtra := 0, 0
	need := func(a, b, d int) { // centres a < b at least d apart
		have := 0
		for i := a; i < b; i++ {
			have += gap[i]
		}
		if have < d {
			gap[b-1] += d - have
		}
	}
	room := func(i, d int, right bool) {
		switch {
		case right && i < n-1:
			need(i, i+1, d)
		case right:
			rightExtra = max(rightExtra, d)
		case i > 0:
			need(i-1, i, d)
		default:
			leftExtra = max(leftExtra, d)
		}
	}
	for _, e := range sq.events {
		lw := maxWidth(e.lines)
		switch e.kind {
		case evMessage:
			a, b := min(e.from, e.to), max(e.from, e.to)
			if a == b {
				room(a, lw+7, true)
			} else {
				need(a, b, lw+4)
			}
		case evNote:
			nw := lw + 4
			switch {
			case e.side == "right":
				room(e.from, nw+3, true)
			case e.side == "left":
				room(e.from, nw+3, false)
			case e.from == e.to:
				l, _ := half(e.from)
				if e.from == 0 {
					leftExtra = max(leftExtra, nw/2-l)
				}
				if e.from == n-1 {
					rightExtra = max(rightExtra, nw-nw/2)
				}
			default:
				need(e.from, e.to, nw-4)
			}
		}
	}
	cx := make([]int, n)
	l0, _ := half(0)
	cx[0] = leftExtra + l0
	for i := 1; i < n; i++ {
		cx[i] = cx[i-1] + gap[i-1]
	}

	// Extents of events and frames, then a shift so nothing is left of 0.
	extent := func(e seqEvent) (int, int) {
		lw := maxWidth(e.lines)
		switch e.kind {
		case evMessage:
			if e.from == e.to {
				return cx[e.from], cx[e.from] + 5 + lw
			}
			return min(cx[e.from], cx[e.to]), max(cx[e.from], cx[e.to])
		case evNote:
			nw := lw + 4
			switch e.side {
			case "right":
				return cx[e.from] + 2, cx[e.from] + 1 + nw
			case "left":
				return cx[e.from] - 1 - nw, cx[e.from] - 2
			}
			a, b := cx[e.from]-2, cx[e.to]+2
			if e.from == e.to {
				a = cx[e.from] - nw/2
				b = a + nw - 1
			}
			if b-a+1 < nw {
				b = a + nw - 1
			}
			return a, b
		}
		return cx[0], cx[0]
	}
	for _, f := range sq.frames {
		f.x0, f.x1 = 1<<30, -1<<30
		f.elseRows, f.elseLabels = nil, nil
	}
	// Frames close in order, so a frame's children are measured first.
	for i, e := range sq.events {
		if e.kind == evMessage || e.kind == evNote {
			a, b := extent(e)
			for f := sq.innermost(i); f >= 0; f = sq.frames[f].parent {
				sq.frames[f].x0, sq.frames[f].x1 = min(sq.frames[f].x0, a), max(sq.frames[f].x1, b)
			}
		}
	}
	for fi := len(sq.frames) - 1; fi >= 0; fi-- {
		f := sq.frames[fi]
		if f.x0 > f.x1 {
			f.x0, f.x1 = cx[0], cx[0]
		}
		f.x0 -= 2
		f.x1 += 2
		if lw := width(f.titleText()) + 3; f.x1-f.x0 < lw {
			f.x1 = f.x0 + lw
		}
		if f.parent >= 0 {
			p := sq.frames[f.parent]
			p.x0, p.x1 = min(p.x0, f.x0), max(p.x1, f.x1)
		}
	}
	lo, hi := 0, 0
	for i := range sq.parts {
		l, r := half(i)
		lo, hi = min(lo, cx[i]-l), max(hi, cx[i]+r)
	}
	hi = max(hi, cx[n-1]+rightExtra)
	for _, e := range sq.events {
		if e.kind == evMessage || e.kind == evNote {
			a, b := extent(e)
			lo, hi = min(lo, a), max(hi, b)
		}
	}
	for _, f := range sq.frames {
		lo, hi = min(lo, f.x0), max(hi, f.x1)
	}
	for i := range cx {
		cx[i] -= lo
	}
	for _, f := range sq.frames {
		f.x0 -= lo
		f.x1 -= lo
	}
	w := hi - lo + 1

	// Rows.
	rows := 4
	for _, e := range sq.events {
		switch e.kind {
		case evMessage:
			if e.from == e.to {
				rows += max(2, len(e.lines))
			} else {
				rows += len(e.lines) + 1
			}
		case evNote:
			rows += len(e.lines) + 2
		default:
			rows++
		}
	}
	rows++
	c := newCanvas(w, rows)
	for i := range n {
		c.edgeLine(cx[i], 2, cx[i], rows-1, styleSolid)
	}
	type note struct {
		x0, y0, x1, y1 int
		lines          []string
	}
	var notes []note
	var texts []func()
	head := func(kind string, right bool) string {
		switch kind {
		case "cross":
			return "x"
		case "arrow":
			switch {
			case ascii && right:
				return ">"
			case ascii:
				return "<"
			case right:
				return "►"
			}
			return "◄"
		}
		return ""
	}
	y := 4
	for _, e := range sq.events {
		switch e.kind {
		case evMessage:
			x, lw := cx[e.from], maxWidth(e.lines)
			if e.from == e.to {
				c.edgeLine(x, y, x+3, y, e.style)
				c.edgeLine(x+3, y, x+3, y+1, e.style)
				c.edgeLine(x+1, y+1, x+3, y+1, e.style)
				if h := head(e.head, false); h != "" {
					c.put(x+1, y+1, h)
				}
				for k, s := range e.lines {
					c.text(x+5, y+k, s)
				}
				y += max(2, len(e.lines))
				continue
			}
			tx := cx[e.to]
			a, b := min(x, tx), max(x, tx)
			for k, s := range e.lines {
				tx, ty := a+(b-a-lw)/2+1, y+k
				texts = append(texts, func() { c.text(tx, ty, s) })
			}
			y += len(e.lines)
			right := tx > x
			end := tx - 1
			if !right {
				end = tx + 1
			}
			c.edgeLine(x, y, end, y, e.style)
			if h := head(e.head, right); h != "" {
				c.put(end, y, h)
			}
			if e.both {
				start := x + 1
				if !right {
					start = x - 1
				}
				c.put(start, y, head("arrow", !right))
			}
			y++
		case evNote:
			a, b := extent(e)
			notes = append(notes, note{a, y, b, y + len(e.lines) + 1, e.lines})
			y += len(e.lines) + 2
		case evFrameStart:
			sq.frames[e.frame].startRow = y
			y++
		case evFrameElse:
			f := sq.frames[e.frame]
			f.elseRows = append(f.elseRows, y)
			f.elseLabels = append(f.elseLabels, e.text)
			y++
		case evFrameEnd:
			sq.frames[e.frame].endRow = y
			y++
		}
	}
	for _, f := range sq.frames {
		c.boxLine(f.x0, f.startRow, f.x1, f.startRow, true)
		c.boxLine(f.x0, f.endRow, f.x1, f.endRow, true)
		c.boxLine(f.x0, f.startRow, f.x0, f.endRow, true)
		c.boxLine(f.x1, f.startRow, f.x1, f.endRow, true)
		for k, r := range f.elseRows {
			c.edgeLine(f.x0, r, f.x1, r, styleDotted)
			if lb := f.elseLabels[k]; lb != "" {
				c.text(f.x0+2, r, "["+truncate(lb, f.x1-f.x0-5, ellipsisFor(ascii))+"]")
			}
		}
	}
	for _, t := range texts {
		t()
	}
	for _, nt := range notes {
		c.clear(nt.x0, nt.y0, nt.x1, nt.y1)
		c.rect(nt.x0, nt.y0, nt.x1, nt.y1, true)
		for k, s := range nt.lines {
			c.text(nt.x0+2, nt.y0+1+k, s)
		}
	}
	for _, f := range sq.frames {
		if t := f.titleText(); t != "" {
			c.text(f.x0+1, f.startRow, " "+truncate(t, f.x1-f.x0-3, ellipsisFor(ascii))+" ")
		}
	}
	for i, p := range sq.parts {
		l, r := half(i)
		c.clear(cx[i]-l, 0, cx[i]+r, 1)
		c.rect(cx[i]-l, 0, cx[i]+r, 2, !p.actor)
		c.text(cx[i]-l+2, 1, p.label)
	}
	return c.lines(ascii)
}

// innermost is the frame that directly holds event i, -1 for none.
func (sq *sequence) innermost(i int) int {
	best := -1
	for f, fr := range sq.frames {
		if fr.start < i && i < fr.end && (best < 0 || fr.start > sq.frames[best].start) {
			best = f
		}
	}
	return best
}

// titleText is the label in a frame's top border.
func (f *seqFrame) titleText() string {
	switch {
	case f.kind == "rect":
		return ""
	case f.label == "":
		return f.kind
	}
	return f.kind + " [" + strings.ReplaceAll(f.label, "\n", " ") + "]"
}

func ellipsisFor(ascii bool) string {
	if ascii {
		return "~"
	}
	return "…"
}
