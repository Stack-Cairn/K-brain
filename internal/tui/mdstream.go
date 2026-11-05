package tui

// Streaming markdown.
//
// A reply is drawn as a run of top-level units: prose split at the blank
// lines that end a top-level block, and fenced code blocks. Each unit renders
// on its own and the units are joined one blank row apart, so a reply's rows
// depend only on its units, never on how its text arrived: streamed a byte at
// a time or set all at once, it draws the same.
//
// A streaming reply only grows, and a unit followed by another one can no
// longer change. mdCache keeps the rows of those frozen units, so each new
// line re-renders the last unit alone. A fence still open at the end of the
// text is the one unit that can grow without bound (a long code listing), so
// it is drawn in groups of lines, and only its last group is redrawn.
//
// What cannot be split: a reference link definition ("[x]: url") may serve
// a link anywhere in the reply, so a reply that has one renders whole, as one
// document.

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// mdUnit is one top-level unit of a markdown source, as byte offsets.
type mdUnit struct {
	// start and end span the unit's lines, end exclusive and without the
	// final newline.
	start, end int
	code       bool
	// For code: the fence's language, and the code's span between its
	// fences. open marks a fence the text has not closed (yet).
	lang          string
	body, bodyEnd int
	open          bool
}

// scanUnits splits s into units, starting at offset from, which must be a
// unit's first line (or 0). Scanning restarts cleanly at any unit, which is
// what lets a streaming reply resume where its frozen units end.
func scanUnits(s string, from int) []mdUnit {
	var units []mdUnit
	prose, last := -1, 0 // the open prose unit's first line, and the end of its last non-blank line
	listy, gap := false, false
	flush := func() {
		if prose >= 0 {
			units = append(units, mdUnit{start: prose, end: last})
		}
		prose, listy, gap = -1, false, false
	}
	for pos := from; pos < len(s); {
		eol, next := lineEnd(s, pos)
		line := s[pos:eol]
		if m := fenceOpenRE.FindStringSubmatch(line); m != nil {
			flush()
			u := mdUnit{start: pos, code: true, lang: m[2], body: next, bodyEnd: len(s), end: len(s), open: true}
			for p := next; p < len(s); {
				e, n := lineEnd(s, p)
				if fenceCloses(s[p:e], m[1]) {
					u.bodyEnd, u.end, u.open = p, e, false
					next = n
					break
				}
				p = n
			}
			if u.open {
				next = len(s)
			}
			units = append(units, u)
			pos = next
			continue
		}
		if strings.TrimSpace(line) == "" {
			gap = prose >= 0
			pos = next
			continue
		}
		if prose >= 0 && gap && startsTopBlock(line, listy) {
			flush()
		}
		if prose < 0 {
			prose = pos
		}
		gap = false
		if isListItem(line) {
			listy = true
		}
		last = eol
		pos = next
	}
	flush()
	return units
}

// lineEnd is the end of the line at pos and the start of the next one.
func lineEnd(s string, pos int) (eol, next int) {
	if i := strings.IndexByte(s[pos:], '\n'); i >= 0 {
		return pos + i, pos + i + 1
	}
	return len(s), len(s)
}

// fenceCloses reports whether line closes a fence opened with fence.
func fenceCloses(line, fence string) bool {
	t := strings.TrimRight(line, " \t")
	trimmed := strings.TrimLeft(t, " ")
	return len(t)-len(trimmed) <= 3 && strings.HasPrefix(trimmed, fence) && strings.Trim(trimmed, fence[:1]) == ""
}

// startsTopBlock reports whether line, coming after a blank line, starts a
// new top-level block: it sits at column 0, and it is not the next item of a
// list already running (splitting a list would change how it is spaced and
// numbered).
func startsTopBlock(line string, listy bool) bool {
	if line[0] == ' ' || line[0] == '\t' {
		return false
	}
	return !listy || !isListItem(line)
}

var listItemRE = regexp.MustCompile(`^(?:[-*+]|\d{1,9}[.)])(?:[ \t]|$)`)

func isListItem(line string) bool { return listItemRE.MatchString(line) }

var refDefRE = regexp.MustCompile(`(?m)^ {0,3}\[[^\]\n]+\]:`)

// codeBody is a code unit's code, as splitFences cut it.
func (u mdUnit) codeBody(s string) string {
	if u.open {
		return s[u.body:]
	}
	if u.bodyEnd <= u.body {
		return ""
	}
	return s[u.body : u.bodyEnd-1]
}

// mdKey is everything a unit's rows depend on besides its text and width.
type mdKey struct {
	gen                             int
	profile                         termenv.Profile
	dark, light, known, mono, ascii bool
	bands                           bool
}

// mdGen counts invalidateMDRenderer calls: a theme change bumps it.
var mdGen int

func currentMDKey() mdKey {
	mdMu.Lock()
	gen, light, known := mdGen, mdLight, mdKnown
	mdMu.Unlock()
	return mdKey{gen: gen, profile: lipgloss.ColorProfile(), dark: lipgloss.HasDarkBackground(),
		light: light, known: known, mono: monoMode, ascii: asciiMode, bands: backgroundsActive()}
}

// mdCache holds a reply's frozen units and their rows at one width.
type mdCache struct {
	width int
	key   mdKey
	// src is the text the frozen units came from, s[:end]; end is where
	// the first unfrozen unit starts.
	src string
	end int
	// units counts the frozen units and rows holds their rows, separator
	// rows included.
	units int
	rows  []string
	// fence holds the finished line groups of a fence still open at the
	// end of the text.
	fence *openFence
	// body blanks rows with nothing visible on them, as a reply's body
	// does; it survives resets.
	body bool
}

// openFence is the drawn part of an open fence: its head row and the rows
// of every line group but the last. done is where the last group starts.
type openFence struct {
	start int
	done  int
	rows  []string
}

// markdownRows is s drawn as markdown at width, from c's frozen units when
// they still match. A nil c renders from scratch.
func markdownRows(c *mdCache, s string, width int) []string {
	if c == nil {
		c = &mdCache{}
	}
	if strings.TrimSpace(s) == "" {
		return appendUnitRows(nil, splitRows(s), true, c.body)
	}
	width = max(width, 8)
	key := currentMDKey()
	if c.width != width || c.key != key || len(s) < c.end || s[:c.end] != c.src {
		*c = mdCache{width: width, key: key, body: c.body}
	}
	if refDefRE.MatchString(s[c.end:]) {
		// Frozen units hold no definition (they would not have frozen), but
		// this one may serve a link anywhere: draw the reply whole.
		*c = mdCache{width: width, key: key, body: c.body}
		return appendUnitRows(nil, splitRows(renderMarkdownWhole(s, width)), true, c.body)
	}
	units := scanUnits(s, c.end)
	for len(units) > 1 {
		c.rows = appendUnitRows(c.rows, unitRows(s, units[0], width), c.units == 0, c.body)
		c.units++
		c.end = units[1].start
		c.fence = nil
		units = units[1:]
	}
	c.src = s[:c.end]
	if len(units) == 0 {
		return append([]string(nil), c.rows...)
	}
	tail := units[0]
	var tr []string
	if tail.code && tail.open {
		tr = c.openFenceRows(s, tail, width)
	} else {
		c.fence = nil
		tr = unitRows(s, tail, width)
	}
	out := make([]string, 0, len(c.rows)+1+len(tr))
	out = append(out, c.rows...)
	return appendUnitRows(out, tr, c.units == 0, c.body)
}

// appendUnitRows adds a unit's rows one blank row below the rows before it.
// Blank rows at the unit's edges go: glamour pads some blocks (lists,
// rules) with a blank row of their own, which inside one document it
// collapses into the gap. The first unit keeps its leading ones, as a whole
// document does. With blank, rows with nothing visible become empty (after
// the trim: a code band's blank last row stays a row).
func appendUnitRows(rows, unit []string, first, blank bool) []string {
	for len(unit) > 0 && unit[len(unit)-1] == "" {
		unit = unit[:len(unit)-1]
	}
	if !first {
		for len(unit) > 0 && unit[0] == "" {
			unit = unit[1:]
		}
	}
	if len(unit) == 0 {
		return rows
	}
	if len(rows) > 0 {
		rows = append(rows, "")
	}
	for _, r := range unit {
		if blank && r != "" && strings.TrimSpace(ansi.Strip(r)) == "" {
			r = ""
		}
		rows = append(rows, r)
	}
	return rows
}

// unitRows draws one unit.
func unitRows(s string, u mdUnit, width int) []string {
	if u.code {
		if u.open {
			return (&mdCache{}).openFenceRows(s, u, width)
		}
		return markdownPost(renderCodeBlock(u.codeBody(s), u.lang, width), width)
	}
	text := s[u.start:u.end]
	out, ok := renderProse(text, width)
	if !ok {
		return splitRows(text)
	}
	return markdownPost(out, width)
}

// renderProse draws prose through glamour.
func renderProse(text string, width int) (string, bool) {
	r := mdRenderer(width)
	if r == nil {
		return "", false
	}
	held, hyphens := holdCodeHyphens(text)
	out, err := r.Render(held)
	if err != nil {
		return "", false
	}
	if hyphens {
		out = strings.ReplaceAll(out, heldHyphen, "-")
	}
	return stripLinePadding(strings.Trim(out, "\n")), true
}

// markdownPost links what a drawn unit names and wraps what overflows.
func markdownPost(out string, width int) []string {
	out = hyperlinkGlamourLinks(out, realFileExists)
	out = linkifyRenderedFilePaths(out, realFileExists)
	return strings.Split(wrapWideLines(out, width), "\n")
}

// codeGroupLines caps a line group of an open fence.
const codeGroupLines = 32

// codeGroups cuts code into line groups: each ends after a blank line or
// after codeGroupLines lines. Cut from any group start, the groups after it
// are the same, so a growing listing redraws only its last group.
func codeGroups(code string) []string {
	var groups []string
	start, n := 0, 0
	for pos := 0; pos < len(code); {
		eol, next := lineEnd(code, pos)
		n++
		if eol == pos || n == codeGroupLines {
			if next < len(code) || eol < len(code) {
				groups = append(groups, code[start:eol])
				start, n = next, 0
			}
		}
		pos = next
		if eol == len(code) {
			break
		}
	}
	return append(groups, code[start:])
}

// openFenceRows draws a fence the text has not closed: head row, then each
// line group highlighted on its own, reusing the finished groups' rows.
func (c *mdCache) openFenceRows(s string, u mdUnit, width int) []string {
	cb := newCodeBand(u.lang, width)
	f := c.fence
	if f == nil || f.start != u.start || f.done < u.body || f.done > len(s) {
		f = &openFence{start: u.start, done: u.body, rows: markdownPost(cb.head(), width)}
		c.fence = f
	}
	groups := codeGroups(s[f.done:])
	for len(groups) > 1 {
		f.rows = append(f.rows, markdownPost(strings.Join(cb.body(groups[0]), "\n"), width)...)
		f.done += len(groups[0]) + 1
		groups = groups[1:]
	}
	out := append([]string(nil), f.rows...)
	out = append(out, markdownPost(strings.Join(cb.body(groups[0]), "\n"), width)...)
	if foot := cb.foot(); foot != "" {
		out = append(out, markdownPost(foot, width)...)
	}
	return out
}

// assistantRows is a reply's body at width: its markdown one indent in,
// the reply glyph on the first row.
func assistantRows(c *mdCache, text string, width int) []string {
	w := width - 2
	if w <= 0 {
		w = 80
	}
	if c == nil {
		c = &mdCache{}
	}
	c.body = true
	rows := markdownRows(c, text, w)
	if len(rows) == 0 {
		rows = []string{""}
	}
	rows[0] = botStyle.Render(glyphAssistant) + dropLeadSpaces(rows[0], 2)
	return rows
}
