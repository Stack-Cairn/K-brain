package mermaid

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// goldenCases render the diagrams under testdata; each golden is the
// rendered lines after a header naming the options.
var goldenCases = []struct {
	name  string
	width int
	ascii bool
}{
	{"flow_decision_cycle", 80, false},
	{"flow_decision_cycle", 80, true},
	{"flow_lr_styles", 80, false},
	{"flow_lr_styles", 80, true},
	{"flow_subgraph_cjk", 80, false},
	{"flow_lr_cycle", 80, false},
	{"flow_wide_fanout", 80, false},
	{"flow_wide_fanout", 0, false},
	{"flow_bt_long_labels", 80, false},
	{"flow_bt_long_labels", 40, false},
	{"flow_rl_link_kinds", 120, false},
	{"flow_nested_subgraphs", 100, false},
	{"flow_crossing_back_edge", 80, false},
	{"seq_frames_notes", 80, false},
	{"seq_frames_notes", 80, true},
	{"seq_cjk_self", 80, false},
	{"seq_cjk_self", 40, false},
}

func TestGoldenRenders(t *testing.T) {
	for _, tc := range goldenCases {
		name := tc.name + "_w" + itoa(tc.width)
		if tc.ascii {
			name += "_ascii"
		}
		t.Run(name, func(t *testing.T) {
			src, err := os.ReadFile(filepath.Join("testdata", tc.name+".mmd"))
			if err != nil {
				t.Fatal(err)
			}
			d, err := Render(string(src), Options{Width: tc.width, ASCII: tc.ascii})
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			var b strings.Builder
			b.WriteString("kind=" + d.Kind + " width=" + itoa(d.FullWidth))
			if d.Truncated {
				b.WriteString(" truncated")
			}
			b.WriteString("\n" + strings.Join(d.Lines, "\n") + "\n")
			got := b.String()
			path := filepath.Join("testdata", name+".golden")
			if *update {
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("missing golden (run with -update): %v", err)
			}
			if got != string(want) {
				t.Errorf("render differs from %s\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
			}
			for i, l := range d.Lines {
				if tc.width > 0 && width(l) > tc.width {
					t.Errorf("line %d is %d cells, over %d", i, width(l), tc.width)
				}
				if tc.ascii {
					for _, r := range l {
						if r > 0x7e && !isWide(r) {
							t.Errorf("line %d has non-ASCII glyph %q in ASCII mode", i, r)
							break
						}
					}
				}
			}
		})
	}
}

// isWide lets CJK label text through the ASCII check: only the drawing has
// to be ASCII.
func isWide(r rune) bool {
	return width(string(r)) == 2 || r == '…' || r == '✓' || r == '（' || r == '）'
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for ; n > 0; n /= 10 {
		s = string(rune('0'+n%10)) + s
	}
	return s
}

func TestRenderIsDeterministic(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("testdata", "*.mmd"))
	if len(files) < 10 {
		t.Fatalf("expected the golden diagrams, found %d", len(files))
	}
	for _, f := range files {
		src, _ := os.ReadFile(f)
		a, err1 := Render(string(src), Options{Width: 90})
		b, err2 := Render(string(src), Options{Width: 90})
		if err1 != nil || err2 != nil {
			t.Fatalf("%s: %v %v", f, err1, err2)
		}
		if strings.Join(a.Lines, "\n") != strings.Join(b.Lines, "\n") {
			t.Errorf("%s renders differently twice", f)
		}
	}
}

func TestCJKBoxesLineUp(t *testing.T) {
	d, err := Render("graph LR\n  a[中文标签] --> b[mixed 混合 text]\n", Options{})
	if err != nil {
		t.Fatal(err)
	}
	// Every row of a single-row LR graph has the same display width: the
	// right border of each box sits in the same column on all three rows.
	w := width(d.Lines[0])
	for i, l := range d.Lines {
		if width(l) != w {
			t.Errorf("row %d is %d cells, row 0 is %d:\n%s", i, width(l), w, strings.Join(d.Lines, "\n"))
		}
	}
	if !strings.Contains(d.Lines[1], "│ 中文标签 ├──►│ mixed 混合 text │") {
		t.Errorf("CJK label not padded by display width:\n%s", strings.Join(d.Lines, "\n"))
	}
}

func TestWideGraphTruncates(t *testing.T) {
	var b strings.Builder
	b.WriteString("graph LR\n")
	for i := range 12 {
		b.WriteString("  n" + itoa(i) + "[Step number " + itoa(i) + "] --> n" + itoa(i+1) + "\n")
	}
	d, err := Render(b.String(), Options{Width: 60})
	if err != nil {
		t.Fatal(err)
	}
	if !d.Truncated || d.FullWidth <= 60 {
		t.Fatalf("truncated=%v full=%d, want a cut drawing", d.Truncated, d.FullWidth)
	}
	for _, l := range d.Lines {
		if width(l) > 60 {
			t.Fatalf("line over width: %q", l)
		}
	}
	if !strings.HasSuffix(d.Lines[1], "…") {
		t.Errorf("cut lines should end in an ellipsis: %q", d.Lines[1])
	}
}

func TestUnsupportedAndErrors(t *testing.T) {
	for _, tc := range []struct {
		src  string
		kind string // "unsupported", "parse", "large"
	}{
		{"pie title Pets\n \"Dogs\" : 386\n", "unsupported"},
		{"classDiagram\n  A <|-- B\n", "unsupported"},
		{"gantt\n title x\n", "unsupported"},
		{"", "unsupported"},
		{"%% only a comment\n", "unsupported"},
		{"graph XY\n A-->B\n", "parse"},
		{"graph TD\n A[unclosed --> B\n", "parse"},
		{"graph TD\n A --> B\n end\n", "parse"},
		{"graph TD\n A --> \n", "parse"},
		{"graph TD\n subgraph S\n A\n end\n S --> B\n", "unsupported"},
		{"sequenceDiagram\n A->>B: hi\n loop x\n", "parse"},
		{"sequenceDiagram\n what is this\n", "parse"},
		{"sequenceDiagram\n else nope\n", "parse"},
		{"graph TD\n" + strings.Repeat("A-->B\n", 20000), "large"},
	} {
		_, err := Render(tc.src, Options{Width: 80})
		var ue *UnsupportedError
		var pe *ParseError
		switch tc.kind {
		case "unsupported":
			if !errors.As(err, &ue) {
				t.Errorf("%q: got %v, want UnsupportedError", tc.src, err)
			}
		case "parse":
			if !errors.As(err, &pe) {
				t.Errorf("%q: got %v, want ParseError", tc.src, err)
			}
		case "large":
			if !errors.Is(err, ErrTooLarge) {
				t.Errorf("got %v, want ErrTooLarge", err)
			}
		}
	}
}

func TestTooManyNodes(t *testing.T) {
	var b strings.Builder
	b.WriteString("graph TD\n")
	for i := range maxNodes + 5 {
		b.WriteString("  x" + itoa(i) + "\n")
	}
	if _, err := Render(b.String(), Options{}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("got %v, want ErrTooLarge", err)
	}
}

func TestKind(t *testing.T) {
	for src, want := range map[string]string{
		"graph TD\nA-->B":                               "graph",
		"%% c\n\nflowchart LR; A-->B":                   "flowchart",
		"---\ntitle: x\n---\nsequenceDiagram\nA->>B: x": "sequenceDiagram",
		"pie\n": "pie",
		"":      "",
	} {
		if got := Kind(src); got != want {
			t.Errorf("Kind(%q) = %q, want %q", src, got, want)
		}
	}
}

func TestFlowchartLinks(t *testing.T) {
	src := `graph TD
  A-->B
  A --- C
  A-.->D
  A ==> E
  A -- text --> F
  A == heavy ==> G
  A -. dots .-> H
  A -->|piped| I
  A --o J
  A --x K
  A <--> L
  A ---->M
  M-->N;N-->O
  P & Q --> R & S
  api-gw --> v1.2
  T:::cls --> U
  V --> V
`
	fc, err := parseFlowchart(skipFrontMatter(sourceLines(src)))
	if err != nil {
		t.Fatal(err)
	}
	type want struct {
		to, label, head, tail string
		style                 lineStyle
	}
	got := map[string]want{}
	for _, e := range fc.edges {
		got[fc.nodes[e.from].id+">"+fc.nodes[e.to].id] = want{fc.nodes[e.to].id, e.label, e.head, e.tail, e.style}
	}
	check := func(key string, w want) {
		t.Helper()
		g, ok := got[key]
		if !ok {
			t.Errorf("missing edge %s (have %v)", key, got)
			return
		}
		if g != w {
			t.Errorf("edge %s = %+v, want %+v", key, g, w)
		}
	}
	check("A>B", want{"B", "", "arrow", "", styleSolid})
	check("A>C", want{"C", "", "", "", styleSolid})
	check("A>D", want{"D", "", "arrow", "", styleDotted})
	check("A>E", want{"E", "", "arrow", "", styleThick})
	check("A>F", want{"F", "text", "arrow", "", styleSolid})
	check("A>G", want{"G", "heavy", "arrow", "", styleThick})
	check("A>H", want{"H", "dots", "arrow", "", styleDotted})
	check("A>I", want{"I", "piped", "arrow", "", styleSolid})
	check("A>J", want{"J", "", "circle", "", styleSolid})
	check("A>K", want{"K", "", "cross", "", styleSolid})
	check("A>L", want{"L", "", "arrow", "arrow", styleSolid})
	check("A>M", want{"M", "", "arrow", "", styleSolid})
	check("M>N", want{"N", "", "arrow", "", styleSolid})
	check("N>O", want{"O", "", "arrow", "", styleSolid})
	for _, k := range []string{"P>R", "P>S", "Q>R", "Q>S", "api-gw>v1.2", "T>U"} {
		if _, ok := got[k]; !ok {
			t.Errorf("missing edge %s", k)
		}
	}
	if v := fc.nodes[fc.byID["V"]]; len(v.loops) != 1 {
		t.Errorf("self edge should become a loop mark, got %+v", v)
	}
}

func TestFlowchartShapes(t *testing.T) {
	fc, err := parseFlowchart(sourceLines(`flowchart TD
  a[rect] --> b(round) --> c([stadium]) --> d[(db)] --> e((circle))
  e --> f{decide} --> g{{hex}} --> h[[sub]] --> i>flag] --> j[/lean/]
  j --> k["quoted [with] brackets"] --> l["line<br/>break"]
`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		label string
		shape shape
	}{
		"a": {"rect", shapeRect}, "b": {"round", shapeRound}, "c": {"stadium", shapeRound},
		"d": {"db", shapeRound}, "e": {"circle", shapeRound}, "f": {"decide", shapeDiamond},
		"g": {"hex", shapeDiamond}, "h": {"sub", shapeRect}, "i": {"flag", shapeRect},
		"j": {"lean", shapeRect}, "k": {"quoted [with] brackets", shapeRect}, "l": {"line\nbreak", shapeRect},
	}
	for id, w := range want {
		n := fc.nodes[fc.byID[id]]
		if n.label != w.label || n.shape != w.shape {
			t.Errorf("node %s = %q/%d, want %q/%d", id, n.label, n.shape, w.label, w.shape)
		}
	}
}

func TestSubgraphMembership(t *testing.T) {
	fc, err := parseFlowchart(sourceLines(`graph TD
  A --> B
  subgraph one [First]
    B --> C
    subgraph two
      D
    end
  end
`))
	if err != nil {
		t.Fatal(err)
	}
	if g := fc.nodes[fc.byID["A"]].group; g != -1 {
		t.Errorf("A first named outside every subgraph, got group %d", g)
	}
	// A node named outside and then inside a subgraph is drawn inside it.
	if g := fc.nodes[fc.byID["B"]].group; g < 0 || fc.groups[g].id != "one" {
		t.Errorf("B belongs to one, got group %d", g)
	}
	if g := fc.nodes[fc.byID["C"]].group; fc.groups[g].id != "one" {
		t.Errorf("C belongs to one, got %d", g)
	}
	d := fc.nodes[fc.byID["D"]].group
	if fc.groups[d].id != "two" || fc.groups[fc.groups[d].parent].title != "First" {
		t.Errorf("D belongs to two inside one, got %+v", fc.groups[d])
	}
}

func TestSequenceParse(t *testing.T) {
	sq, err := parseSequence(sourceLines(`sequenceDiagram
  participant A as Alice
  actor B
  A->>+B: call
  B-->>-A: reply
  A-xB: lost
  A-)B: async
  A->B: plain
  A-->B: dotted
  A<<->>B: both
  activate A
  Note over A,B: shared
  box Group
  participant C
  end
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(sq.parts) != 3 || sq.parts[0].label != "Alice" || !sq.parts[1].actor {
		t.Fatalf("participants = %+v", sq.parts)
	}
	type msg struct {
		head  string
		style lineStyle
		both  bool
	}
	var got []msg
	for _, e := range sq.events {
		if e.kind == evMessage {
			got = append(got, msg{e.head, e.style, e.both})
		}
	}
	want := []msg{
		{"arrow", styleSolid, false}, {"arrow", styleDotted, false}, {"cross", styleSolid, false},
		{"arrow", styleSolid, false}, {"", styleSolid, false}, {"", styleDotted, false}, {"arrow", styleSolid, true},
	}
	if len(got) != len(want) {
		t.Fatalf("messages = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("message %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestWrapAndTruncate(t *testing.T) {
	if got := wrapText("the quick brown fox", 9); strings.Join(got, "|") != "the quick|brown fox" {
		t.Errorf("wrap words: %q", got)
	}
	if got := wrapText("中文字符很长的标签", 6); strings.Join(got, "|") != "中文字|符很长|的标签" {
		t.Errorf("wrap CJK: %q", got)
	}
	if got := wrapText("supercalifragilistic", 8); strings.Join(got, "|") != "supercal|ifragili|stic" {
		t.Errorf("wrap long word: %q", got)
	}
	if got := truncate("中文字符", 5, "…"); got != "中文…" || width(got) != 5 {
		t.Errorf("truncate CJK: %q", got)
	}
	if got := truncate("abc", 5, "…"); got != "abc" {
		t.Errorf("truncate short: %q", got)
	}
}

func TestCanvasJunctions(t *testing.T) {
	c := newCanvas(5, 3)
	c.edgeLine(0, 1, 4, 1, styleSolid)
	c.edgeLine(2, 0, 2, 2, styleSolid)
	if got := strings.Join(c.lines(false), "|"); got != "  │|──┼──|  │" {
		t.Errorf("unicode crossing: %q", got)
	}
	if got := strings.Join(c.lines(true), "|"); got != "  ||--+--|  |" {
		t.Errorf("ascii crossing: %q", got)
	}
	c = newCanvas(3, 3)
	c.rect(0, 0, 2, 2, true)
	if got := strings.Join(c.lines(false), "|"); got != "┌─┐|│ │|└─┘" {
		t.Errorf("sharp rect: %q", got)
	}
	c = newCanvas(3, 3)
	c.rect(0, 0, 2, 2, false)
	if got := strings.Join(c.lines(false), "|"); got != "╭─╮|│ │|╰─╯" {
		t.Errorf("round rect: %q", got)
	}
	c = newCanvas(4, 1)
	if n := c.text(0, 0, "中文"); n != 4 || c.lines(false)[0] != "中文" {
		t.Errorf("wide text took %d cells: %q", n, c.lines(false)[0])
	}
}

// FuzzRender checks that no input panics or overflows the width.
func FuzzRender(f *testing.F) {
	files, _ := filepath.Glob(filepath.Join("testdata", "*.mmd"))
	for _, p := range files {
		src, _ := os.ReadFile(p)
		f.Add(string(src), 60)
	}
	f.Add("graph TD\nA-->B-->C-->A\nsubgraph x\nB\nend", 30)
	f.Add("sequenceDiagram\nA->>A: x\nNote left of A: y\nalt\nelse\nend", 20)
	f.Fuzz(func(t *testing.T, src string, w int) {
		w = w%200 + 1
		if w < 1 {
			w = -w + 1
		}
		d, err := Render(src, Options{Width: w})
		if err != nil {
			return
		}
		for _, l := range d.Lines {
			if width(l) > w {
				t.Fatalf("line wider than %d: %q", w, l)
			}
		}
	})
}
