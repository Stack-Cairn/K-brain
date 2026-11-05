package tui

import (
	"math"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// transcriptView is the transcript's scroll window. It keeps the method set
// of the bubbles viewport it replaces (Width, Height, YOffset, SetYOffset,
// AtBottom, GotoBottom, ScrollUp, Update, View, …), but it never holds the
// transcript as one string: it reads rows out of the line index refreshVP
// maintains, so a frame costs the rows on screen rather than the whole
// session.
type transcriptView struct {
	Width, Height int
	// YOffset is the first transcript row on screen.
	YOffset int

	// ix is the line index; nil until the first refreshVP.
	ix *txIndex
	// static is content set through SetContent, for callers (mostly tests)
	// that drive the window without blocks.
	static []string
}

// mouseWheelDelta is how many rows one wheel notch scrolls.
const mouseWheelDelta = 3

var transcriptKeys = viewport.DefaultKeyMap()

// TotalLineCount is the number of transcript rows.
func (v transcriptView) TotalLineCount() int {
	if v.ix != nil {
		return v.ix.total
	}
	return len(v.static)
}

func (v transcriptView) maxYOffset() int { return max(0, v.TotalLineCount()-v.Height) }

func (v transcriptView) AtTop() bool    { return v.YOffset <= 0 }
func (v transcriptView) AtBottom() bool { return v.YOffset >= v.maxYOffset() }

// ScrollPercent is how far down the window is, from 0 to 1.
func (v transcriptView) ScrollPercent() float64 {
	t := v.TotalLineCount()
	if v.Height >= t {
		return 1.0
	}
	return math.Max(0, math.Min(1, float64(v.YOffset)/float64(t-v.Height)))
}

// SetContent replaces the window's content with fixed text, detaching it
// from the transcript until the next refreshVP.
func (v *transcriptView) SetContent(s string) {
	v.ix = nil
	v.static = strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	if v.YOffset > len(v.static)-1 {
		v.GotoBottom()
	}
}

func (v *transcriptView) SetYOffset(n int) { v.YOffset = max(0, min(n, v.maxYOffset())) }
func (v *transcriptView) GotoTop()         { v.SetYOffset(0) }
func (v *transcriptView) GotoBottom()      { v.SetYOffset(v.maxYOffset()) }

func (v *transcriptView) ScrollDown(n int) {
	if v.AtBottom() || n == 0 || v.TotalLineCount() == 0 {
		return
	}
	v.SetYOffset(v.YOffset + n)
}

func (v *transcriptView) ScrollUp(n int) {
	if v.AtTop() || n == 0 || v.TotalLineCount() == 0 {
		return
	}
	v.SetYOffset(v.YOffset - n)
}

func (v *transcriptView) PageDown()     { v.ScrollDown(v.Height) }
func (v *transcriptView) PageUp()       { v.ScrollUp(v.Height) }
func (v *transcriptView) HalfPageDown() { v.ScrollDown(v.Height / 2) }
func (v *transcriptView) HalfPageUp()   { v.ScrollUp(v.Height / 2) }

// Update scrolls for the pager keys and the mouse wheel, as the bubbles
// viewport did.
func (v transcriptView) Update(msg tea.Msg) (transcriptView, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, transcriptKeys.PageDown):
			v.PageDown()
		case key.Matches(msg, transcriptKeys.PageUp):
			v.PageUp()
		case key.Matches(msg, transcriptKeys.HalfPageDown):
			v.HalfPageDown()
		case key.Matches(msg, transcriptKeys.HalfPageUp):
			v.HalfPageUp()
		case key.Matches(msg, transcriptKeys.Down):
			v.ScrollDown(1)
		case key.Matches(msg, transcriptKeys.Up):
			v.ScrollUp(1)
		}
	case tea.MouseMsg:
		if msg.Action != tea.MouseActionPress || msg.Shift {
			break
		}
		switch msg.Button { //nolint:exhaustive
		case tea.MouseButtonWheelUp:
			v.ScrollUp(mouseWheelDelta)
		case tea.MouseButtonWheelDown:
			v.ScrollDown(mouseWheelDelta)
		}
	}
	return v, nil
}

// visibleLines is the rows from YOffset down, at most Height of them, in a
// slice the caller may change.
func (v transcriptView) visibleLines() []string {
	top := max(0, v.YOffset)
	if v.ix == nil {
		bottom := max(min(top+v.Height, len(v.static)), top)
		if top >= len(v.static) {
			return nil
		}
		return append([]string(nil), v.static[top:bottom]...)
	}
	return v.ix.rows(top, top+v.Height)
}

// View is the window padded to its full size, exactly as the bubbles
// viewport drew it, so the frame around it is unchanged.
func (v transcriptView) View() string {
	lines := v.visibleLines()
	if w := v.Width; w > 0 {
		for i, l := range lines {
			if ansi.StringWidth(l) > w {
				lines[i] = ansi.Cut(l, 0, w)
			}
		}
	}
	return lipgloss.NewStyle().
		Width(v.Width).
		Height(v.Height).
		MaxHeight(v.Height).
		MaxWidth(v.Width).
		Render(strings.Join(lines, "\n"))
}

// txEntry is one shown block in the line index: where it starts and the
// rendered rows it draws. Blocks the transcript hides (the startup card once
// a conversation starts) have no entry.
type txEntry struct {
	uid uint64
	bi  int // index into model.blocks at the last refresh
	gap int // blank separator rows above the block: 0 (glued) or 1
	y0  int // first row of the block itself
	// lines are the block's rendered rows, shared with its cache.
	lines []string
}

func (e *txEntry) top() int { return e.y0 - e.gap }
func (e *txEntry) end() int { return e.y0 + len(e.lines) }

// txIndex is the transcript as rows: an ordered list of entries whose y0
// values are the prefix sums of the heights above them. Finding a row is a
// binary search, so drawing a window never walks the whole transcript.
type txIndex struct {
	entries []txEntry
	total   int

	// The key the rows were laid out under. A change to any of these lays
	// out every block again.
	width        int
	ancient      bool
	bannerHidden bool
	valid        bool

	// uids[i] is the uid model.blocks[i] had at the last refresh and ent[i]
	// its entry (-1 when hidden). refreshVP compares them to find the first
	// block that changed and lays out only from there.
	uids []uint64
	ent  []int
}

// find is the entry holding row r, its separator row included, or -1.
func (ix *txIndex) find(r int) int {
	if r < 0 || r >= ix.total || len(ix.entries) == 0 {
		return -1
	}
	i := sort.Search(len(ix.entries), func(i int) bool { return ix.entries[i].end() > r })
	if i >= len(ix.entries) || ix.entries[i].top() > r {
		return -1
	}
	return i
}

// row is transcript row r: a line of a block, or "" for a separator.
func (ix *txIndex) row(r int) string {
	i := ix.find(r)
	if i < 0 {
		return ""
	}
	e := &ix.entries[i]
	if r < e.y0 {
		return ""
	}
	return e.lines[r-e.y0]
}

// rows is transcript rows [from, to), clipped to the transcript.
func (ix *txIndex) rows(from, to int) []string {
	to = min(to, ix.total)
	if from >= to {
		return nil
	}
	out := make([]string, 0, to-from)
	i := ix.find(from)
	for r := from; r < to && i >= 0 && i < len(ix.entries); {
		e := &ix.entries[i]
		for ; r < e.y0 && r < to; r++ {
			out = append(out, "")
		}
		for ; r < e.end() && r < to; r++ {
			out = append(out, e.lines[r-e.y0])
		}
		i++
	}
	return out
}

// txAnchor pins the scroll position to a row of one block, so a reflow above
// it or a resize keeps the same content at the top of the window.
type txAnchor struct {
	uid  uint64
	line int // row within the block; -1 is its separator row
	of   int // the block's height when the anchor was taken
	at   int // the block's entry when the anchor was taken
	ok   bool
}

func (ix *txIndex) anchorAt(r int) txAnchor {
	if ix == nil || !ix.valid {
		return txAnchor{}
	}
	i := ix.find(r)
	if i < 0 {
		return txAnchor{}
	}
	e := &ix.entries[i]
	return txAnchor{uid: e.uid, line: r - e.y0, of: len(e.lines), at: i, ok: true}
}

// rowOf is where an anchor's row is now, given the block's new entry.
func (a txAnchor) rowOf(e *txEntry) int {
	switch {
	case a.line < 0:
		return e.top()
	case a.of == len(e.lines) || a.of <= 0:
		return e.y0 + min(a.line, len(e.lines)-1)
	default:
		// The block reflowed: keep the same fraction of it above the window.
		return e.y0 + min(a.line*len(e.lines)/a.of, len(e.lines)-1)
	}
}
