package mermaid

import (
	"regexp"
	"strings"
	"unicode"
)

// shape is how a node's border is drawn.
type shape uint8

const (
	shapeRect shape = iota
	shapeRound
	shapeDiamond
)

// fcNode is a flowchart node.
type fcNode struct {
	id    string
	label string
	shape shape
	// group is the index of its innermost subgraph, -1 for none.
	group int
	// seen records that a statement inside a subgraph named it, which
	// settles its group.
	seen bool
	// loops are self edges, drawn as a mark inside the node.
	loops []string
}

// fcEdge is a flowchart edge in source order.
type fcEdge struct {
	from, to   int
	label      string
	style      lineStyle
	head, tail string // "arrow", "circle", "cross" or ""
}

// fcGroup is a subgraph.
type fcGroup struct {
	id, title string
	parent    int
}

// flowchart is a parsed flowchart.
type flowchart struct {
	dir    string // TD, BT, LR or RL
	nodes  []*fcNode
	byID   map[string]int
	edges  []fcEdge
	groups []fcGroup
}

var fcHeaderRE = regexp.MustCompile(`(?i)^(?:graph|flowchart|flowchart-elk)(?:\s+(TB|TD|BT|LR|RL))?\s*;?\s*$`)

func parseFlowchart(lines []srcLine) (*flowchart, error) {
	m := fcHeaderRE.FindStringSubmatch(lines[0].text)
	if m == nil {
		// "graph TD; A-->B" on one line.
		head, rest, ok := strings.Cut(lines[0].text, ";")
		if ok {
			if m = fcHeaderRE.FindStringSubmatch(head); m != nil {
				lines = append([]srcLine{{n: lines[0].n, text: head}, {n: lines[0].n, text: rest}}, lines[1:]...)
			}
		}
		if m == nil {
			return nil, &ParseError{Line: lines[0].n, Msg: "unknown flowchart direction"}
		}
	}
	fc := &flowchart{dir: strings.ToUpper(m[1]), byID: map[string]int{}}
	if fc.dir == "" || fc.dir == "TB" {
		fc.dir = "TD"
	}
	var stack []int
	for _, l := range lines[1:] {
		for _, stmt := range splitStatements(l.text) {
			if err := fc.statement(stmt, l.n, &stack); err != nil {
				return nil, err
			}
		}
	}
	if len(fc.nodes) == 0 {
		return nil, &ParseError{Line: lines[0].n, Msg: "no nodes"}
	}
	if len(fc.nodes) > maxNodes {
		return nil, ErrTooLarge
	}
	return fc, nil
}

// splitStatements splits a line at semicolons outside brackets, quotes and
// edge labels.
func splitStatements(s string) []string {
	var out []string
	depth, quote, pipe := 0, false, false
	start := 0
	for i, r := range s {
		switch {
		case r == '"':
			quote = !quote
		case quote:
		case r == '|':
			pipe = !pipe
		case pipe:
		case strings.ContainsRune("[({", r):
			depth++
		case strings.ContainsRune("])}", r):
			depth = max(depth-1, 0)
		case r == ';' && depth == 0:
			if t := strings.TrimSpace(s[start:i]); t != "" {
				out = append(out, t)
			}
			start = i + 1
		}
	}
	if t := strings.TrimSpace(s[start:]); t != "" {
		out = append(out, t)
	}
	return out
}

var ignoredFlowStmt = regexp.MustCompile(`^(?:style|classDef|class|linkStyle|click|direction|accTitle|accDescr|title)\b`)

func (fc *flowchart) statement(s string, line int, stack *[]int) error {
	switch {
	case s == "end":
		if len(*stack) == 0 {
			return &ParseError{Line: line, Msg: "end without subgraph"}
		}
		*stack = (*stack)[:len(*stack)-1]
		return nil
	case strings.HasPrefix(s, "subgraph ") || s == "subgraph":
		return fc.subgraph(strings.TrimSpace(strings.TrimPrefix(s, "subgraph")), line, stack)
	case ignoredFlowStmt.MatchString(s):
		return nil
	}
	p := &fcParser{s: s, line: line, fc: fc, stack: *stack}
	return p.chain()
}

func (fc *flowchart) subgraph(rest string, line int, stack *[]int) error {
	parent := -1
	if n := len(*stack); n > 0 {
		parent = (*stack)[n-1]
	}
	id, title := rest, rest
	if i := strings.IndexAny(rest, "["); i > 0 {
		id = strings.TrimSpace(rest[:i])
		t := strings.TrimSpace(rest[i:])
		t = strings.TrimSuffix(strings.TrimPrefix(t, "["), "]")
		title = unquote(strings.TrimSpace(t))
	} else {
		title = unquote(rest)
		id = title
	}
	if id == "" {
		id = "subgraph" + string(rune('0'+len(fc.groups)%10))
	}
	fc.groups = append(fc.groups, fcGroup{id: id, title: cleanLabel(title), parent: parent})
	*stack = append(*stack, len(fc.groups)-1)
	return nil
}

// fcParser reads one statement: node groups joined by links.
type fcParser struct {
	s     string
	pos   int
	line  int
	fc    *flowchart
	stack []int
}

func (p *fcParser) fail(msg string) error { return &ParseError{Line: p.line, Msg: msg} }

func (p *fcParser) skipSpace() {
	for p.pos < len(p.s) && (p.s[p.pos] == ' ' || p.s[p.pos] == '\t') {
		p.pos++
	}
}

func (p *fcParser) chain() error {
	from, err := p.nodeGroup()
	if err != nil {
		return err
	}
	for {
		p.skipSpace()
		if p.pos >= len(p.s) {
			return nil
		}
		e, ok := p.link()
		if !ok {
			return p.fail("expected a link after " + strings.TrimSpace(p.s[:p.pos]))
		}
		to, err := p.nodeGroup()
		if err != nil {
			return err
		}
		for _, a := range from {
			for _, b := range to {
				e.from, e.to = a, b
				if a == b {
					n := p.fc.nodes[a]
					n.loops = append(n.loops, e.label)
					continue
				}
				p.fc.edges = append(p.fc.edges, e)
			}
		}
		from = to
	}
}

func (p *fcParser) nodeGroup() ([]int, error) {
	var out []int
	for {
		p.skipSpace()
		n, err := p.node()
		if err != nil {
			return nil, err
		}
		out = append(out, n)
		p.skipSpace()
		if p.pos < len(p.s) && p.s[p.pos] == '&' {
			p.pos++
			continue
		}
		return out, nil
	}
}

func isIDRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// node reads an id with an optional shape and class.
func (p *fcParser) node() (int, error) {
	start := p.pos
	for p.pos < len(p.s) {
		r, size := decodeRune(p.s[p.pos:])
		if isIDRune(r) {
			p.pos += size
			continue
		}
		// A dash or dot inside an id ("api-gw", "v1.2") but not the start
		// of a link ("A-->B", "A-.->B").
		if (r == '-' || r == '.') && p.pos > start {
			nr, _ := decodeRune(p.s[p.pos+size:])
			if isIDRune(nr) {
				p.pos += size
				continue
			}
		}
		break
	}
	id := p.s[start:p.pos]
	if id == "" {
		return 0, p.fail("expected a node id")
	}
	for _, g := range p.fc.groups {
		if g.id == id {
			return 0, &UnsupportedError{Kind: "flowchart", Reason: "edges to subgraphs are not drawn in the terminal"}
		}
	}
	label, sh, hasShape, err := p.shape()
	if err != nil {
		return 0, err
	}
	if strings.HasPrefix(p.s[p.pos:], ":::") {
		p.pos += 3
		for p.pos < len(p.s) {
			r, size := decodeRune(p.s[p.pos:])
			if !isIDRune(r) && r != '-' {
				break
			}
			p.pos += size
		}
	}
	idx, ok := p.fc.byID[id]
	if !ok {
		idx = len(p.fc.nodes)
		p.fc.byID[id] = idx
		p.fc.nodes = append(p.fc.nodes, &fcNode{id: id, label: id, group: -1})
	}
	n := p.fc.nodes[idx]
	if hasShape {
		n.label, n.shape = label, sh
	}
	if len(p.stack) > 0 && !n.seen {
		n.group, n.seen = p.stack[len(p.stack)-1], true
	}
	return idx, nil
}

// shapes pairs openers with their closers, longest opener first.
var shapes = []struct {
	open, close string
	shape       shape
}{
	{"(((", ")))", shapeRound},
	{"((", "))", shapeRound},
	{"([", "])", shapeRound},
	{"[[", "]]", shapeRect},
	{"[(", ")]", shapeRound},
	{"{{", "}}", shapeDiamond},
	{"[/", "/]", shapeRect},
	{"[/", "\\]", shapeRect},
	{"[\\", "\\]", shapeRect},
	{"[\\", "/]", shapeRect},
	{"[", "]", shapeRect},
	{"(", ")", shapeRound},
	{"{", "}", shapeDiamond},
	{">", "]", shapeRect},
}

func (p *fcParser) shape() (string, shape, bool, error) {
	rest := p.s[p.pos:]
	for _, sh := range shapes {
		if !strings.HasPrefix(rest, sh.open) {
			continue
		}
		body := rest[len(sh.open):]
		text := ""
		end := -1
		if strings.HasPrefix(strings.TrimLeft(body, " "), `"`) {
			lead := len(body) - len(strings.TrimLeft(body, " "))
			q := strings.Index(body[lead+1:], `"`)
			if q < 0 {
				return "", 0, false, p.fail("unclosed quote")
			}
			text = body[lead+1 : lead+1+q]
			after := body[lead+1+q+1:]
			trimmed := strings.TrimLeft(after, " ")
			if !strings.HasPrefix(trimmed, sh.close) {
				continue
			}
			end = len(body) - len(trimmed)
		} else {
			end = strings.Index(body, sh.close)
			if end < 0 {
				continue
			}
			text = body[:end]
		}
		p.pos += len(sh.open) + end + len(sh.close)
		return cleanLabel(text), sh.shape, true, nil
	}
	if rest != "" && strings.ContainsRune("[({", rune(rest[0])) {
		return "", 0, false, p.fail("unclosed node shape")
	}
	return "", 0, false, nil
}

// link reads an edge: its body, an optional label in the body ("-- x -->")
// or after it ("-->|x|").
func (p *fcParser) link() (fcEdge, bool) {
	start := p.pos
	e := fcEdge{}
	rest := p.s[p.pos:]
	if strings.HasPrefix(rest, "<") {
		e.tail = "arrow"
		p.pos++
	} else if len(rest) > 2 && (rest[0] == 'o' || rest[0] == 'x') && (rest[1] == '-' || rest[1] == '=' || rest[1] == '.') {
		e.tail = map[byte]string{'o': "circle", 'x': "cross"}[rest[0]]
		p.pos++
	}
	bodyStart := p.pos
	for p.pos < len(p.s) && strings.ContainsRune("-=.", rune(p.s[p.pos])) {
		p.pos++
	}
	body := p.s[bodyStart:p.pos]
	if len(body) < 2 && !(body == "-" && p.pos < len(p.s) && p.s[p.pos] == '>') {
		p.pos = start
		return e, false
	}
	head := p.readHead()
	if head == "" && (body == "--" || body == "==" || body == "-.") {
		// "A -- text --> B": the text runs to the closing part.
		if label, end, closer, ok := textLink(p.s[p.pos:], body); ok {
			e.label = cleanLabel(label)
			p.pos += end
			body += closer
			head = closerHead(closer)
		}
	}
	e.head = head
	e.style = styleSolid
	switch {
	case strings.Contains(body, "="):
		e.style = styleThick
	case strings.Contains(body, "."):
		e.style = styleDotted
	}
	p.skipSpace()
	if p.pos < len(p.s) && p.s[p.pos] == '|' {
		end := strings.IndexByte(p.s[p.pos+1:], '|')
		if end < 0 {
			p.pos = start
			return e, false
		}
		e.label = cleanLabel(p.s[p.pos+1 : p.pos+1+end])
		p.pos += end + 2
	}
	return e, true
}

// readHead reads an arrowhead, circle or cross after a link body.
func (p *fcParser) readHead() string {
	if p.pos >= len(p.s) {
		return ""
	}
	switch p.s[p.pos] {
	case '>':
		p.pos++
		return "arrow"
	case 'o', 'x':
		// Only as a head when a space or the end follows ("A --o B").
		if p.pos+1 == len(p.s) || p.s[p.pos+1] == ' ' {
			h := map[byte]string{'o': "circle", 'x': "cross"}[p.s[p.pos]]
			p.pos++
			return h
		}
	}
	return ""
}

var textLinkClosers = map[string]*regexp.Regexp{
	"--": regexp.MustCompile(`\s*(-{2,}>|-{3,}|-{2,}[ox](?:\s|$))`),
	"==": regexp.MustCompile(`\s*(={2,}>|={3,}|={2,}[ox](?:\s|$))`),
	"-.": regexp.MustCompile(`\s*(\.-+>|\.-+[ox](?:\s|$)|\.-+)`),
}

// textLink finds where the text of a "-- text -->" link ends. It returns
// the text, the offset just past the closing part and the closing part.
func textLink(s, open string) (string, int, string, bool) {
	re := textLinkClosers[open]
	loc := re.FindStringSubmatchIndex(s)
	if loc == nil || loc[0] == 0 {
		return "", 0, "", false
	}
	text := strings.TrimSpace(s[:loc[0]])
	if text == "" {
		return "", 0, "", false
	}
	closer := strings.TrimSpace(s[loc[2]:loc[3]])
	return text, loc[3], closer, true
}

// closerHead is the end a text link's closing part draws.
func closerHead(closer string) string {
	switch {
	case strings.HasSuffix(closer, ">"):
		return "arrow"
	case strings.HasSuffix(closer, "o"):
		return "circle"
	case strings.HasSuffix(closer, "x"):
		return "cross"
	}
	return ""
}

var brRE = regexp.MustCompile(`(?i)<br\s*/?>`)

// cleanLabel unquotes a label, turns <br> into line breaks and drops
// markdown emphasis markers and entity codes it cannot show.
func cleanLabel(s string) string {
	s = unquote(strings.TrimSpace(s))
	s = brRE.ReplaceAllString(s, "\n")
	s = strings.NewReplacer("#quot;", `"`, "#amp;", "&", "#lt;", "<", "#gt;", ">", "**", "", "`", "").Replace(s)
	var lines []string
	for l := range strings.SplitSeq(s, "\n") {
		lines = append(lines, strings.Join(strings.Fields(l), " "))
	}
	return strings.Join(lines, "\n")
}

func unquote(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

func decodeRune(s string) (rune, int) {
	for _, r := range s {
		return r, len(string(r))
	}
	return 0, 0
}
