package tui

import (
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
	"github.com/rivo/uniseg"
)

// Mouse editing in the composer: a click puts the cursor where it lands, a
// drag selects, a double click selects a word and a triple click (or Alt+A)
// selects the whole draft. With a selection, Backspace / Delete remove it,
// typing or pasting replaces it and Ctrl+X cuts it.

// composerSel is a selected rune range [a, b) of the draft. It belongs to the
// draft it was made on: once the draft changes it no longer applies.
type composerSel struct {
	a, b int
	val  string
}

// multiClickWindow is how close together clicks on the same spot must come
// to count as a double or triple click.
const multiClickWindow = 450 * time.Millisecond

// inputRow is one visual row of the composer as the textarea wraps it.
type inputRow struct {
	off   int    // rune offset into Value() of the row's first rune
	runes []rune // the row's runes, the wrap's trailing space included
	last  bool   // the last row of its logical line
}

// realLen is how many of the row's runes are in the draft: the last row of a
// line carries one space the wrap adds after the line's end.
func (r inputRow) realLen() int {
	if r.last {
		return max(len(r.runes)-1, 0)
	}
	return len(r.runes)
}

// inputLayout is the draft as the textarea lays it out: every logical line
// wrapped to the textarea's width, top to bottom.
func (m *model) inputLayout() []inputRow {
	width := max(m.input.Width(), 1)
	var rows []inputRow
	off := 0
	for line := range strings.SplitSeq(m.input.Value(), "\n") {
		runes := []rune(line)
		wrapped := textareaWrap(runes, width)
		for i, w := range wrapped {
			rows = append(rows, inputRow{off: off, runes: w, last: i == len(wrapped)-1})
			// The wrap's extra space after the line's end stands where the
			// newline is, so the next line starts right after it.
			off += len(w)
		}
	}
	return rows
}

// inputScroll is the first layout row the textarea shows: 0 unless the draft
// is taller than the textarea, in which case the rows on screen are matched
// against the layout.
func (m *model) inputScroll(rows []inputRow) int {
	h := len(m.inputLines)
	if len(rows) <= h || h == 0 {
		return 0
	}
	prompt := m.input.Prompt
	plain := func(r inputRow) string {
		return strings.ReplaceAll(strings.TrimRight(prompt+string(r.runes), " \t"), chipSentinel, "")
	}
	shown := make([]string, h)
	for i, l := range m.inputLines {
		shown[i] = strings.ReplaceAll(l, chipSentinel, "")
	}
	for top := 0; top+h <= len(rows); top++ {
		ok := true
		for i := 0; i < h && ok; i++ {
			ok = plain(rows[top+i]) == shown[i]
		}
		if ok {
			return top
		}
	}
	// No exact match (a styled or masked view): the textarea keeps the cursor
	// on screen, so assume it sits on the last row shown.
	cur := m.cursorLayoutRow(rows)
	return min(max(cur-h+1, 0), len(rows)-h)
}

// cursorLayoutRow is the layout row the cursor is on.
func (m *model) cursorLayoutRow(rows []inputRow) int {
	off := m.cursorOffset(m.input.Value())
	for i, r := range rows {
		if off < r.off+r.realLen() || (off == r.off+r.realLen() && r.last) {
			return i
		}
	}
	return max(len(rows)-1, 0)
}

// runeCells is how many cells r takes, as the textarea measures it.
func runeCells(r rune) int { return uniseg.StringWidth(string(r)) }

// inputOffsetAt maps a cell of the composer (row into inputLines, column
// from the composer's left edge, prompt included) to a rune offset into the
// draft. end picks the offset after a character whose cell is hit only partly
// (a wide character's right half), which is how a selection's far edge reads.
func (m *model) inputOffsetAt(row, col int, end bool) int {
	if m.input.Value() == "" {
		return 0
	}
	rows := m.inputLayout()
	vr := m.inputScroll(rows) + row
	if vr >= len(rows) {
		return len([]rune(m.input.Value()))
	}
	vr = max(vr, 0)
	r := rows[vr]
	x := col - ansi.StringWidth(m.input.Prompt)
	if x < 0 {
		return r.off
	}
	acc := 0
	for i, ru := range r.runes[:r.realLen()] {
		w := runeCells(ru)
		if x < acc+w {
			if end && x > acc {
				return r.off + i + 1
			}
			return r.off + i
		}
		acc += w
	}
	// Past the row's end: its end, or for a soft-wrapped row, just before the
	// space it wrapped at, so the cursor stays on the row clicked.
	n := r.realLen()
	if !r.last && n > 0 && !end {
		n--
	}
	return r.off + n
}

// inputSelRange is the rune range a drag over the composer covers.
func (m *model) inputSelRange(s selection) (int, int) {
	lo, hi := selOrder(s)
	a := m.inputOffsetAt(lo.row, lo.col, false)
	b := m.inputOffsetAt(hi.row, hi.col, true)
	return a, max(a, b)
}

// composerSelection is the live selection of the draft, if there is one.
func (m *model) composerSelection() (int, int, bool) {
	if m.cmp == nil || m.cmp.sel == nil {
		return 0, 0, false
	}
	s := m.cmp.sel
	if s.val != m.input.Value() || s.a >= s.b {
		return 0, 0, false
	}
	n := len([]rune(s.val))
	return min(s.a, n), min(s.b, n), true
}

// setComposerSel selects [a, b) of the current draft; an empty range clears
// the selection.
func (m *model) setComposerSel(a, b int) {
	c := m.composer()
	if a >= b {
		c.sel = nil
		return
	}
	c.sel = &composerSel{a: a, b: b, val: m.input.Value()}
}

func (m *model) clearComposerSel() {
	if m.cmp != nil {
		m.cmp.sel = nil
	}
}

// composerSelText is the selected text with chip marks dropped and pasted
// chips expanded, for the clipboard.
func (m *model) composerSelText() string {
	a, b, ok := m.composerSelection()
	if !ok {
		return ""
	}
	runes := []rune(m.input.Value())
	a, b = widenToChips(runes, a, b)
	return strings.ReplaceAll(m.expandPasteChips(string(runes[a:b])), chipSentinel, "")
}

// selectAllComposer selects the whole draft (Alt+A, triple click).
func (m *model) selectAllComposer() {
	n := len([]rune(m.input.Value()))
	m.setComposerSel(0, n)
	m.placeCursor(n)
}

// selectWordAt selects the word around rune offset off (double click).
func (m *model) selectWordAt(off int) {
	runes := []rune(m.input.Value())
	if len(runes) == 0 {
		return
	}
	off = min(max(off, 0), len(runes))
	class := func(r rune) int {
		switch {
		case r == '\n':
			return -1
		case unicode.IsSpace(r):
			return 0
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_':
			return 1
		default:
			return 2
		}
	}
	at := off
	if at == len(runes) || runes[at] == '\n' {
		at--
	}
	if at < 0 || class(runes[at]) < 0 {
		return
	}
	k := class(runes[at])
	a, b := at, at+1
	for a > 0 && class(runes[a-1]) == k {
		a--
	}
	for b < len(runes) && class(runes[b]) == k {
		b++
	}
	a, b = widenToChips(runes, a, b)
	m.setComposerSel(a, b)
	m.placeCursor(b)
}

// deleteComposerRange removes [a, b) from the draft, widened so no chip is
// left half cut, and leaves the cursor where it was.
func (m *model) deleteComposerRange(a, b int) {
	runes := []rune(m.input.Value())
	a, b = widenToChips(runes, max(a, 0), min(b, len(runes)))
	if a >= b {
		return
	}
	m.setComposer(string(runes[:a])+string(runes[b:]), a)
	m.clearComposerSel()
	m.refreshMenu()
}

// composerSelKey acts on a key while part of the draft is selected. It runs
// after the composer has noted the draft for undo. done says the key is used
// up; otherwise it carries on and, for typing, lands where the selection was.
func (m *model) composerSelKey(msg tea.KeyMsg) (cmd tea.Cmd, done bool) {
	a, b, ok := m.composerSelection()
	if !ok {
		m.clearComposerSel()
		return nil, false
	}
	m.clearComposerSel()
	switch {
	case (msg.Type == tea.KeyBackspace || msg.Type == tea.KeyDelete) && !msg.Alt:
		m.deleteComposerRange(a, b)
		return nil, true
	case msg.Type == tea.KeyCtrlX:
		m.setComposerSel(a, b)
		text := m.composerSelText()
		c := m.composer()
		c.kills = append(c.kills, text)
		if len(c.kills) > maxKills {
			c.kills = c.kills[1:]
		}
		m.deleteComposerRange(a, b)
		return copyTextCmd(text), true
	case msg.Type == tea.KeyEsc && m.menu == nil:
		return nil, true
	case msg.Type == tea.KeyLeft && !msg.Alt:
		m.placeCursor(a)
		return nil, true
	case msg.Type == tea.KeyRight && !msg.Alt:
		m.placeCursor(b)
		return nil, true
	case msg.Type == tea.KeyRunes && !msg.Alt, msg.Type == tea.KeySpace, msg.Type == tea.KeyCtrlY:
		m.deleteComposerRange(a, b)
		// The replacement is its own undo step, not part of a typing run.
		m.composer().runOpen = false
		return nil, false
	}
	return nil, false
}

// copyTextCmd copies text to the clipboard off the event loop.
func copyTextCmd(text string) tea.Cmd {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	clip := clipboardFor()
	return func() tea.Msg {
		route, err := clip.copyText(text)
		return copyDoneMsg{chars: len([]rune(text)), route: route, err: err}
	}
}

// composerClick handles a left click on the composer that did not drag:
// one click puts the cursor there, two select a word, three the whole draft.
func (m *model) composerClick(p selPos) {
	now := m.nowFn()
	c := m.composer()
	if now.Sub(c.clickAt) <= multiClickWindow && c.clickRow == p.row && abs(c.clickCol-p.col) <= 1 {
		c.clicks++
	} else {
		c.clicks = 1
	}
	c.clickAt, c.clickRow, c.clickCol = now, p.row, p.col
	off := m.inputOffsetAt(p.row, p.col, false)
	switch {
	case c.clicks == 2:
		m.selectWordAt(off)
	case c.clicks >= 3:
		m.selectAllComposer()
		c.clicks = 0
	default:
		m.clearComposerSel()
		m.placeCursor(off)
	}
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// highlightComposerSel paints the composer selection onto the textarea's
// view, whose rows start with the prompt.
func (m *model) highlightComposerSel(iv string) string {
	a, b, ok := m.composerSelection()
	if !ok {
		return iv
	}
	lines := strings.Split(iv, "\n")
	rows := m.inputLayout()
	top := m.inputScroll(rows)
	pw := ansi.StringWidth(m.input.Prompt)
	for i := range lines {
		vr := top + i
		if vr >= len(rows) {
			break
		}
		r := rows[vr]
		start, end, col := -1, -1, pw
		for j, ru := range r.runes[:r.realLen()] {
			w := runewidth.RuneWidth(ru)
			if o := r.off + j; o >= a && o < b {
				if start < 0 {
					start = col
				}
				end = col + w
			}
			col += w
		}
		// A selected newline or wrap point shows as one cell past the text.
		if nl := r.off + r.realLen(); start < 0 && nl >= a && nl < b && r.last {
			start, end = col, col+1
		} else if start >= 0 && nl >= a && nl < b && r.last {
			end = col + 1
		}
		if start >= 0 {
			lines[i] = reverseRange(lines[i], start, end)
		}
	}
	return strings.Join(lines, "\n")
}

// textareaWrap wraps a line exactly as the textarea does (bubbles v1
// textarea's word wrap), so cells on screen map back to runes. Whitespace
// becomes spaces one for one, and the last row gets one extra trailing space.
func textareaWrap(runes []rune, width int) [][]rune {
	var (
		lines  = [][]rune{{}}
		word   = []rune{}
		row    int
		spaces int
	)
	for _, r := range runes {
		if unicode.IsSpace(r) {
			spaces++
		} else {
			word = append(word, r)
		}
		if spaces > 0 {
			if uniseg.StringWidth(string(lines[row]))+uniseg.StringWidth(string(word))+spaces > width {
				row++
				lines = append(lines, []rune{})
			}
			lines[row] = append(lines[row], word...)
			lines[row] = append(lines[row], []rune(strings.Repeat(" ", spaces))...)
			spaces = 0
			word = nil
		} else {
			lastCharLen := runewidth.RuneWidth(word[len(word)-1])
			if uniseg.StringWidth(string(word))+lastCharLen > width {
				if len(lines[row]) > 0 {
					row++
					lines = append(lines, []rune{})
				}
				lines[row] = append(lines[row], word...)
				word = nil
			}
		}
	}
	spaces++
	if uniseg.StringWidth(string(lines[row]))+uniseg.StringWidth(string(word))+spaces-1 >= width {
		lines = append(lines, []rune{})
		row++
	}
	lines[row] = append(lines[row], word...)
	lines[row] = append(lines[row], []rune(strings.Repeat(" ", spaces))...)
	return lines
}
