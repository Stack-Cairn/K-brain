package tui

import (
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
	if !clamp && (y < max(m.viewportTop, 0) || y >= m.viewportTop+m.viewportRows || x < 0 || x >= m.width) {
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

func (m *model) contentLine(r int) string {
	for i := range m.blocks {
		b := &m.blocks[i]
		if r < b.y0 || r > b.y1 {
			continue
		}
		rows := strings.Split(ansi.Strip(b.rendered), "\n")
		if r-b.y0 < len(rows) {
			return strings.TrimRight(rows[r-b.y0], " \t")
		}
		return ""
	}
	return ""
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

func (m *model) highlightInput(iv string) string {
	if m.sel == nil || !m.sel.anchor.input {
		return iv
	}
	lines := strings.Split(iv, "\n")
	lo, hi := selOrder(*m.sel)
	for r := lo.row; r <= hi.row && r < len(lines); r++ {
		start, end := selCols(lo, hi, r, ansi.StringWidth(lines[r]))
		lines[r] = reverseRange(lines[r], start, end)
	}
	return strings.Join(lines, "\n")
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

func (m *model) handleMouseSelect(msg tea.MouseMsg) (handled bool, cmd tea.Cmd) {
	switch msg.Action {
	case tea.MouseActionPress:
		m.sel = nil
		if msg.Button != tea.MouseButtonLeft {
			return false, nil
		}

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
		if m.sel.anchor != m.sel.cur {
			// A drag selects text: keep the highlight so the terminal's native
			// copy (ctrl+shift+c / cmd+c) can grab it. No auto-copy, no notice.
			m.sel.done = true
			return true, nil
		}
		inputClick := m.sel.anchor.input
		m.sel = nil
		if inputClick {
			return true, nil
		}
		m.clickAt(msg.X, msg.Y)
		return true, nil
	}
	return false, nil
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
	row := p.row
	for i := range m.blocks {
		if row < m.blocks[i].y0 || row > m.blocks[i].y1 {
			continue
		}

		if m.blocks[i].toggle() {
			m.refreshVP()
			return
		}
	}
}
