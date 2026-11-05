package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// streamReply is a reply with every kind of top-level block, including the
// ones a split must not cut: a loose list, a list item's indented code and
// a fence with blank lines inside.
const streamReply = "## Plan\n\n" +
	"The cache is keyed by **width**, so a resize drops it. See `cache.go` and [the docs](https://example.com/docs).\n\n" +
	"- first item\n- second item\n\n- a loose third item\n\n" +
	"1. step one\n2. step two\n\n" +
	"   with an indented continuation paragraph.\n\n" +
	"```go\nfunc f() {\n\tx := 1\n\n\treturn x\n}\n```\n\n" +
	"> a quote line\n> another line\n\n" +
	"| key | value |\n|-----|-------|\n| a   | 1     |\n| b   | 2     |\n\n" +
	"---\n\n" +
	"Final paragraph with a long sentence that wraps across the narrow widths the golden test uses."

// streamInto feeds text to m as a running turn does, in chunks of size n.
func streamInto(m *model, text string, n int) {
	for len(text) > 0 {
		k := min(n, len(text))
		m.Update(textMsg(text[:k]))
		text = text[k:]
	}
	m.Update(turnDoneMsg{})
}

func TestScanUnitsSplitsAtTopLevelBlocks(t *testing.T) {
	var got []string
	for _, u := range scanUnits(streamReply, 0) {
		got = append(got, streamReply[u.start:u.end])
	}
	want := []string{
		"## Plan",
		"The cache is keyed by **width**, so a resize drops it. See `cache.go` and [the docs](https://example.com/docs).",
		"- first item\n- second item\n\n- a loose third item\n\n1. step one\n2. step two\n\n   with an indented continuation paragraph.",
		"```go\nfunc f() {\n\tx := 1\n\n\treturn x\n}\n```",
		"> a quote line\n> another line",
		"| key | value |\n|-----|-------|\n| a   | 1     |\n| b   | 2     |",
		"---",
		"Final paragraph with a long sentence that wraps across the narrow widths the golden test uses.",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("units:\n%q\nwant:\n%q", got, want)
	}
	// Resuming at any unit's start finds the same units after it.
	units := scanUnits(streamReply, 0)
	for i, u := range units {
		if rest := scanUnits(streamReply, u.start); !slices.Equal(rest, units[i:]) {
			t.Fatalf("scan resumed at unit %d differs", i)
		}
	}
}

func TestScanUnitsOpenFence(t *testing.T) {
	s := "intro\n\n```py\nx = 1\n\ny = 2"
	units := scanUnits(s, 0)
	if len(units) != 2 || !units[1].code || !units[1].open || units[1].codeBody(s) != "x = 1\n\ny = 2" {
		t.Fatalf("units = %+v", units)
	}
	closed := s + "\n```"
	units = scanUnits(closed, 0)
	if units[1].open || units[1].codeBody(closed) != "x = 1\n\ny = 2" {
		t.Fatalf("closed fence = %+v body %q", units[1], units[1].codeBody(closed))
	}
}

// The unit-by-unit layout draws what one whole-document render drew, for
// every block kind. The one change is spacing: the whole document left two
// blank rows between a code block and a block glamour pads (a quote, list or
// rule), and the units leave one, as everywhere else.
func TestMarkdownUnitsMatchWholeDocument(t *testing.T) {
	collapse := func(rows []string) []string {
		var out []string
		for i, r := range rows {
			if r == "" && i > 0 && rows[i-1] == "" {
				continue
			}
			out = append(out, r)
		}
		return out
	}
	for _, w := range []int{38, 78, 118} {
		got := strings.Split(renderMarkdown(streamReply, w), "\n")
		want := collapse(strings.Split(renderMarkdownWhole(streamReply, w), "\n"))
		if !slices.Equal(got, want) {
			t.Errorf("width %d: unit layout differs from the whole document\n got:\n%s\nwant:\n%s", w, ansi.Strip(strings.Join(got, "\n")), ansi.Strip(strings.Join(want, "\n")))
		}
		if !slices.Equal(got, collapse(got)) {
			t.Errorf("width %d: two blank rows in a row", w)
		}
	}
}

// After every streamed line the reply's rows, partly from frozen units, are
// the rows a fresh render of the same text draws.
func TestStreamedRowsMatchFreshRenderAtEveryStep(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(80, 24))
	m.busy = true
	text := streamReply + "\n\n```sh\n" + strings.Repeat("echo line\n", 80) + "echo last"
	for _, line := range strings.SplitAfter(text, "\n") {
		m.Update(textMsg(line))
		b := &m.blocks[len(m.blocks)-1]
		if b.kind != blockAssistant {
			continue
		}
		got := b.linesAt(m.width, false)
		if want := assistantRows(nil, b.text, m.width); !slices.Equal(got, want) {
			t.Fatalf("after %q the streamed rows differ from a fresh render\n got:\n%s\nwant:\n%s",
				line, ansi.Strip(strings.Join(got, "\n")), ansi.Strip(strings.Join(want, "\n")))
		}
	}
}

// A reference definition may serve a link in any unit, so a reply with one
// draws whole, streamed or not.
func TestStreamedReplyWithReferenceLinkDrawsWhole(t *testing.T) {
	text := "See [the guide][g] first.\n\nThen the rest.\n\n[g]: https://example.com/guide"
	c := &mdCache{}
	var rows []string
	for i := range strings.Count(text, "\n") + 1 {
		upto := strings.Join(strings.SplitAfter(text, "\n")[:i+1], "")
		rows = markdownRows(c, strings.TrimSuffix(upto, "\n"), 60)
	}
	if want := strings.Split(renderMarkdownWhole(text, 60), "\n"); !slices.Equal(rows, want) {
		t.Fatalf("got:\n%s\nwant:\n%s", ansi.Strip(strings.Join(rows, "\n")), ansi.Strip(strings.Join(want, "\n")))
	}
}

// TestGoldenStreamedReplyMatchesWhole streams one reply in chunks of several
// sizes and sets it all at once; every way must draw the same frame, in every
// colour mode and at every width. Both write to the same golden.
func TestGoldenStreamedReplyMatchesWhole(t *testing.T) {
	t.Chdir(t.TempDir())
	defer SetLightTheme(false)
	for _, mode := range goldenModes {
		for _, size := range goldenSizes {
			name := fmt.Sprintf("stream_reply_%s_%dx%d", mode.name, size[0], size[1])
			t.Run(name, func(t *testing.T) {
				restore := useProfile(t, mode.profile, mode.dark)
				defer restore()
				SetLightTheme(!mode.dark)
				if mode.mono {
					setMonoMode(true, true)
				}
				setASCIIGlyphs(mode.ascii)

				frame := func(build func(m *model)) string {
					m := compactCmdModel()
					m.applyAppearance()
					m.Update(mkWinSize(size[0], size[1]))
					build(m)
					m.layout()
					return m.View()
				}
				whole := frame(func(m *model) { m.appendAssistantBlock(streamReply) })
				checkGolden(t, name+".ansi", whole)
				checkGolden(t, name+".txt", stripView(whole))
				for _, n := range []int{1, 7, 64, len(streamReply)} {
					got := frame(func(m *model) {
						m.busy = true
						streamInto(m, streamReply, n)
					})
					if got != whole {
						t.Fatalf("streamed in %d-byte chunks the frame differs from the reply set at once\n got:\n%s\nwant:\n%s", n, stripView(got), stripView(whole))
					}
				}
			})
		}
	}
}
