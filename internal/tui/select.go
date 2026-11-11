package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

type selPos struct {
	row, col int
	input    bool
}

type selection struct {
	anchor, cur selPos
	done        bool
}

func (m *model) selPoint(x, y int, clamp bool) (selPos, bool) {
	if len(m.blocks) == 0 || m.viewH == 0 || m.viewportRows == 0 {
		return selPos{}, false
	}
	if !clamp && (y < max(m.viewportTop, m.viewTop+m.vpTopRows(), 0) || y >= m.viewportTop+m.viewportRows || x < 0 || x >= m.width) {
		return selPos{}, false
	}
	row := y - m.viewTop - m.vpTopRows() + m.vp.YOffset + m.vpLead
	// The banner block is skipped from the transcript once the conversation
	// has messages, so anchor on the first rendered block instead of index 0.
	first := 0
	for _, blk := range m.blocks {
		if blk.y0 >= 0 {
			first = blk.y0
			break
		}
	}
	last := m.blocks[len(m.blocks)-1].y1
	if !clamp && (row < first || row > last) {
		return selPos{}, false
	}
	row = max(min(row, last), first)
	w := ansi.StringWidth(m.contentLine(row))
	return selPos{row: row, col: max(min(x-m.vpXOff(), w), 0)}, true
}

func (m *model) inInputRow(y int) bool {
	return m.inputTop >= 0 && y >= m.inputTop && y < m.inputTop+len(m.inputLines)
}

func (m *model) inputPoint(x, y int, clamp bool) (selPos, bool) {
	if m.inputTop < 0 || len(m.inputLines) == 0 {
		return selPos{}, false
	}
	x -= m.inputLeft
	row := y - m.inputTop
	if !clamp && (row < 0 || row >= len(m.inputLines)) {
		return selPos{}, false
	}
	row = max(min(row, len(m.inputLines)-1), 0)
	w := ansi.StringWidth(m.inputLines[row])
	return selPos{row: row, col: max(min(x, w), 0), input: true}, true
}

// contentLine is transcript row r as plain text, trailing blanks trimmed.
func (m *model) contentLine(r int) string {
	bi := m.blockAtRow(r)
	if bi < 0 {
		return ""
	}
	b := &m.blocks[bi]
	return b.plainRow(r - b.y0)
}

func cellSlice(s string, off, n int) string {
	var b strings.Builder
	col := 0
	for _, r := range s {
		// Match the width convention of ansi.StringWidth used by selPoint so
		// ambiguous glyphs (e.g. ●) never shift the selection off-by-one.
		w := ansi.StringWidth(string(r))
		if col+w > off && col < off+n {
			b.WriteRune(r)
		}
		col += w
		if col >= off+n {
			break
		}
	}
	return b.String()
}

func (m *model) selText(s selection) string {
	if s.anchor.input {
		return m.composerSelText()
	}
	lo, hi := selOrder(s)
	var lines []string
	for r := lo.row; r <= hi.row; r++ {
		var ln string
		if lo.input {
			if r < len(m.inputLines) {
				ln = m.inputLines[r]
			}
		} else {
			ln = m.contentLine(r)
		}
		start, end := selCols(lo, hi, r, ansi.StringWidth(ln))
		lines = append(lines, strings.TrimRight(cellSlice(ln, start, end-start), " \t"))
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

func selOrder(s selection) (lo, hi selPos) {
	lo, hi = s.anchor, s.cur
	if lo.row > hi.row || (lo.row == hi.row && lo.col > hi.col) {
		lo, hi = hi, lo
	}
	return lo, hi
}

func selCols(lo, hi selPos, r, lineWidth int) (int, int) {
	start, end := 0, lineWidth
	if r == lo.row {
		start = lo.col
	}
	if r == hi.row {
		end = hi.col
	}
	return start, max(end, start)
}

// highlightInput paints the composer selection, which a drag over the
// composer keeps up to date.
func (m *model) highlightInput(iv string) string {
	if m.namePrompt != nil && m.namePrompt.mask {
		return iv
	}
	return m.highlightComposerSel(iv)
}

func (m *model) highlightSelection(view string) string {
	if m.sel == nil || m.sel.anchor.input {
		return view
	}
	lines := strings.Split(view, "\n")
	lo, hi := selOrder(*m.sel)
	base := -m.vp.YOffset
	for r := lo.row; r <= hi.row; r++ {
		si := r + base
		if si < 0 || si >= len(lines) {
			continue
		}
		start, end := selCols(lo, hi, r, ansi.StringWidth(lines[si]))
		lines[si] = reverseRange(lines[si], start, end)
	}
	return strings.Join(lines, "\n")
}

func reverseRange(line string, start, end int) string {
	if start >= end {
		return line
	}
	var b strings.Builder
	col := 0
	on := false
	for i := 0; i < len(line); {
		if line[i] == 0x1b {
			j := i + 1
			if j < len(line) && line[j] == '[' {
				for j++; j < len(line) && (line[j] < 0x40 || line[j] > 0x7e); j++ {
				}
				j++
			} else if j < len(line) && line[j] == ']' {

				for j++; j < len(line); j++ {
					if line[j] == '\a' {
						j++
						break
					}
					if line[j] == 0x1b && j+1 < len(line) && line[j+1] == '\\' {
						j += 2
						break
					}
				}
			} else if j < len(line) {
				j++
			}
			b.WriteString(line[i:j])
			if on {

				b.WriteString("\x1b[7m")
			}
			i = j
			continue
		}
		r, size := utf8.DecodeRuneInString(line[i:])
		if !on && col >= start && col < end {
			b.WriteString("\x1b[7m")
			on = true
		} else if on && col >= end {
			b.WriteString("\x1b[27m")
			on = false
		}
		b.WriteRune(r)
		col += runewidth.RuneWidth(r)
		i += size
	}
	if on {
		b.WriteString("\x1b[27m")
	}
	return b.String()
}

type selScrollTick struct{}

// copyDoneMsg reports a finished selection copy.
type copyDoneMsg struct {
	chars int
	route copyRoute
	err   error
}

// copySelection copies the current selection off the event loop, since the
// native clipboard tools can take a moment to start.
func (m *model) copySelection() tea.Cmd {
	if m.sel == nil {
		return nil
	}
	text := m.selText(*m.sel)
	if strings.TrimSpace(text) == "" {
		return nil
	}
	clip := clipboardFor()
	return func() tea.Msg {
		route, err := clip.copyText(text)
		return copyDoneMsg{chars: utf8.RuneCountInString(text), route: route, err: err}
	}
}

func (m *model) copyNotice(msg copyDoneMsg) string {
	switch {
	case msg.err != nil:
		return fmt.Sprintf(m.tr("Copy failed: %s"), msg.err.Error())
	case msg.route == copyOSC52:
		return fmt.Sprintf(m.tr("Sent %d characters to the terminal clipboard"), msg.chars)
	default:
		return fmt.Sprintf(m.tr("Copied %d characters"), msg.chars)
	}
}

// isSuperC reports Cmd+C (super+c) as sent by terminals speaking the kitty
// keyboard protocol, e.g. "\x1b[99;9u". Other terminals keep Cmd+C to
// themselves, which is why a drag copies on release.
func isSuperC(rendered string) bool {
	mm := csiURe.FindStringSubmatch(rendered)
	if mm == nil {
		return false
	}
	var b []byte
	for f := range strings.FieldsSeq(mm[1]) {
		n, err := strconv.ParseUint(f, 10, 8)
		if err != nil {
			return false
		}
		b = append(b, byte(n))
	}
	body, ok := strings.CutSuffix(string(b), "u")
	if !ok {
		return false
	}
	codeStr, modStr, _ := strings.Cut(body, ";")
	codeStr, _, _ = strings.Cut(codeStr, ":")
	modStr, _, _ = strings.Cut(modStr, ":")
	code, err := strconv.Atoi(codeStr)
	if err != nil || (code != 'c' && code != 'C') {
		return false
	}
	mods, err := strconv.Atoi(modStr)
	if err != nil || mods < 1 {
		return false
	}
	mods--
	return mods&8 != 0 && mods&(2|4) == 0
}

func (m *model) handleMouseSelect(msg tea.MouseMsg) (handled bool, cmd tea.Cmd) {
	switch msg.Action {
	case tea.MouseActionPress:
		m.sel = nil
		if msg.Button != tea.MouseButtonLeft {
			return false, nil
		}
		m.clearComposerSel()

		if p, ok := m.inputPoint(msg.X, msg.Y, false); ok {
			m.sel = &selection{anchor: p, cur: p}
			return true, nil
		}
		p, ok := m.selPoint(msg.X, msg.Y, false)
		if !ok {
			return false, nil
		}
		m.sel = &selection{anchor: p, cur: p}
		return true, nil
	case tea.MouseActionMotion:
		if m.sel == nil || m.sel.done {
			return false, nil
		}

		if m.sel.anchor.input {
			if p, ok := m.inputPoint(msg.X, msg.Y, true); ok {
				m.sel.cur = p
				if m.inputSelectable() {
					m.setComposerSel(m.inputSelRange(*m.sel))
				}
			}
			return true, nil
		}
		if p, ok := m.selPoint(msg.X, msg.Y, true); ok {
			m.sel.cur = p
		}
		m.selDragX, m.selDragY = msg.X, msg.Y
		return true, m.selEdgeScroll()
	case tea.MouseActionRelease:
		if m.sel == nil || m.sel.done {
			return false, nil
		}
		if m.sel.anchor.input {
			return true, m.releaseInputSel()
		}
		if m.sel.anchor != m.sel.cur {
			// Mouse capture keeps the terminal from selecting anything itself,
			// so a finished drag copies the highlighted text and keeps the
			// highlight until the next key or click.
			m.sel.done = true
			return true, m.copySelection()
		}
		m.sel = nil
		m.clickAt(msg.X, msg.Y)
		return true, nil
	}
	return false, nil
}

// releaseInputSel ends a press on the composer: a drag keeps its selection
// (Backspace deletes it, typing replaces it) and copies it, as a transcript
// drag does; a click places the cursor or, repeated, selects.
func (m *model) releaseInputSel() tea.Cmd {
	s := *m.sel
	if !m.inputSelectable() {
		m.sel = nil
		return nil
	}
	if _, _, ok := m.composerSelection(); ok && s.anchor != s.cur {
		m.sel.done = true
		if m.inputEditable() {
			// The cursor goes where the drag ended.
			backward := s.cur.row < s.anchor.row || (s.cur.row == s.anchor.row && s.cur.col < s.anchor.col)
			m.placeCursor(m.inputOffsetAt(s.cur.row, s.cur.col, !backward))
		}
		return m.copySelection()
	}
	m.sel = nil
	if m.inputEditable() {
		m.composerClick(s.anchor)
	}
	return nil
}

// inputSelectable reports whether a drag over the composer selects (and
// copies) the draft: it shows the draft itself, not a masked prompt or the
// placeholder. A masked value is never selected or copied.
func (m *model) inputSelectable() bool {
	return !(m.namePrompt != nil && m.namePrompt.mask) && !m.ancientInput()
}

// inputEditable reports whether clicks also move the composer's cursor and
// select words or everything: only while typing goes to the composer, not
// while a dialog or card (a permission prompt, an MCP form, ...) takes keys.
func (m *model) inputEditable() bool {
	return m.inputSelectable() && !m.overlayOwnsKeys()
}

func (m *model) selEdgeScroll() tea.Cmd {
	if m.sel == nil || m.sel.done {
		return nil
	}
	top := max(m.viewportTop, 0)
	bottom := m.viewportTop + m.viewportRows - 1
	switch {
	// The transcript now starts at the top of the terminal, so there is no row
	// above it to drag into — dragging onto the first transcript row scrolls up.
	case m.selDragY <= top && m.vp.YOffset > 0:
		m.vp.SetYOffset(m.vp.YOffset - 1)
	case m.selDragY > bottom && !m.vp.AtBottom():
		m.vp.SetYOffset(m.vp.YOffset + 1)
	default:
		return nil
	}
	m.follow = m.vp.AtBottom()
	if p, ok := m.selPoint(m.selDragX, m.selDragY, true); ok {
		m.sel.cur = p
	}
	return tea.Tick(60*time.Millisecond, func(time.Time) tea.Msg { return selScrollTick{} })
}

func (m *model) clickAt(x, y int) {
	if m.palette != nil || m.menu != nil || m.viewH == 0 {
		return
	}
	p, ok := m.selPoint(x, y, false)
	if !ok {
		return
	}
	if bi := m.blockAtRow(p.row); bi >= 0 && m.blocks[bi].toggle() {
		m.refreshVP()
	}
}
