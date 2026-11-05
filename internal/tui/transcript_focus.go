package tui

// Transcript focus.
//
// Alt+↑ takes the keyboard from the composer to the transcript (it yields
// to the queue pane, where Alt+↑ moves a queued message). One block is
// focused at a time, marked down its left edge: j/k move between blocks,
// h/l fold and open the focused one, e (or Enter, Space) flips it, E folds
// or opens every tool result and thinking run at once, v opens it full
// screen (blockviewer.go), and / searches the transcript, as /find does.
// Esc gives the keyboard back. A bar above the composer says where focus is
// and which keys apply.
//
// Folding goes through the block API: a kind with a toggle (tool output,
// thinking, whatever registers one) folds the way a click does; any other
// block taller than two rows folds to its first row and a count.
//
// Search runs over what the transcript draws, smart-case (a capital letter
// makes it case-sensitive). A folded block whose hidden text matches counts
// as one match on its first row and opens when the search reaches it.

import (
	"fmt"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// txFocus is the transcript's keyboard focus.
type txFocus struct {
	// uid is the focused block.
	uid uint64
	// typing is set while a search query is being typed into draft.
	typing bool
	draft  string
	find   *txFind
}

// txFind is the current search: its query, its matches as of the last
// search key, and the match last jumped to.
type txFind struct {
	query string
	hits  []txHit
	cur   int
}

// txHit is a match: a row of a block, or row -1 for a folded block whose
// hidden text matches.
type txHit struct {
	uid uint64
	row int
}

// transcriptKey is the transcript's keys: the block viewer's while one is
// open, the focus keys while the transcript has focus, and Alt+↑, which
// gives it focus. ok is false for a key it leaves to the rest of dispatch.
func (m *model) transcriptKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	tx := m.txs()
	if msg.Type == tea.KeyCtrlC {
		return m, nil, false
	}
	switch {
	case tx.viewer == nil && tx.focus == nil:
		if msg.Type == tea.KeyUp && msg.Alt && m.canFocusTranscript() {
			m.focusTranscript()
			return m, nil, true
		}
	case msg.Type == tea.KeyRunes && len(msg.Runes) > 1 && !msg.Paste:
		// Keys typed faster than the terminal is read arrive together:
		// each is its own command here.
		for _, r := range msg.Runes {
			m.transcriptKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}, Alt: msg.Alt})
		}
		return m, nil, true
	case tx.viewer != nil:
		r, cmd := m.blockViewerKey(msg)
		return r, cmd, true
	case tx.focus != nil:
		r, cmd := m.transcriptFocusKey(msg)
		return r, cmd, true
	}
	return m, nil, false
}

// canFocusTranscript reports whether Alt+↑ is the transcript's here: not in
// a subagent view, the dock, the history search or the queue pane.
func (m *model) canFocusTranscript() bool {
	return m.taskVP == nil && !m.tasksFocus && !m.queueFocused() && m.composer().hs == nil && m.lastFocusable() >= 0
}

// focusTranscript gives the transcript focus, on the last block that starts
// on screen.
func (m *model) focusTranscript() {
	tx := m.txs()
	bi := m.lastFocusable()
	bottom := m.vp.YOffset + m.vp.Height
	for i := bi; i >= 0; i-- {
		if b := &m.blocks[i]; focusable(b) && b.y0 < bottom {
			bi = i
			break
		}
	}
	if bi < 0 {
		return
	}
	tx.focus = &txFocus{uid: m.blocks[bi].uid}
	m.revealBlock(bi)
}

// blurTranscript gives the keyboard back to the composer.
func (m *model) blurTranscript() {
	m.txs().focus = nil
}

func focusable(b *block) bool { return b.y0 >= 0 && b.kind != blockBanner }

func (m *model) lastFocusable() int {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if focusable(&m.blocks[i]) {
			return i
		}
	}
	return -1
}

// focusIndex is the focused block's index, or -1. A focused block that has
// gone (a /clear, a rewind) moves focus to the last block.
func (m *model) focusIndex() int {
	f := m.txs().focus
	if f == nil {
		return -1
	}
	for i := range m.blocks {
		if m.blocks[i].uid == f.uid && focusable(&m.blocks[i]) {
			return i
		}
	}
	bi := m.lastFocusable()
	if bi >= 0 {
		f.uid = m.blocks[bi].uid
	}
	return bi
}

// transcriptFocusKey handles a key while the transcript has focus. Every
// key is the transcript's: one it does not bind does nothing.
func (m *model) transcriptFocusKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	f := m.txs().focus
	if f.typing {
		m.findTypingKey(msg)
		return m, nil
	}
	bi := m.focusIndex()
	if bi < 0 {
		m.blurTranscript()
		return m, nil
	}
	k := msg.String()
	switch {
	case msg.Type == tea.KeyEsc || k == "q":
		m.blurTranscript()
	case msg.Type == tea.KeyDown || k == "j":
		m.moveFocus(bi, 1)
	case msg.Type == tea.KeyUp || k == "k":
		m.moveFocus(bi, -1)
	case msg.Type == tea.KeyHome || k == "g":
		m.moveFocus(-1, 1)
	case msg.Type == tea.KeyEnd || k == "G":
		m.moveFocus(len(m.blocks), -1)
	case msg.Type == tea.KeyPgUp:
		m.vp.PageUp()
		m.follow = false
		m.focusOnScreen(bi, -1)
	case msg.Type == tea.KeyPgDown:
		m.vp.PageDown()
		m.follow = m.vp.AtBottom()
		m.focusOnScreen(bi, 1)
	case msg.Type == tea.KeyLeft || k == "h":
		m.foldFocused(bi, false)
	case msg.Type == tea.KeyRight || k == "l":
		m.foldFocused(bi, true)
	case msg.Type == tea.KeyEnter || msg.Type == tea.KeySpace || k == "e":
		m.foldFocused(bi, !blockOpen(&m.blocks[bi]))
	case k == "E":
		m.foldAll()
		m.revealBlock(m.focusIndex())
	case k == "v":
		m.openBlockViewer(bi)
	case k == "/":
		f.typing, f.draft = true, ""
	case k == "n":
		m.findStep(-1)
	case k == "N":
		m.findStep(1)
	}
	return m, nil
}

// moveFocus moves focus from block from to the next focusable block in
// direction d (-1 up, 1 down), staying put at either end.
func (m *model) moveFocus(from, d int) {
	for i := from + d; i >= 0 && i < len(m.blocks); i += d {
		if focusable(&m.blocks[i]) {
			m.txs().focus.uid = m.blocks[i].uid
			m.revealBlock(i)
			return
		}
	}
}

// focusOnScreen keeps focus visible after a page scroll: a focused block
// scrolled out of the window gives focus to the first block on screen (d >
// 0) or the last (d < 0).
func (m *model) focusOnScreen(bi, d int) {
	top, bottom := m.vp.YOffset, m.vp.YOffset+m.vp.Height
	if b := &m.blocks[bi]; b.y1 >= top && b.y0 < bottom {
		return
	}
	pick := -1
	for i := range m.blocks {
		b := &m.blocks[i]
		if !focusable(b) || b.y1 < top || b.y0 >= bottom {
			continue
		}
		pick = i
		if d > 0 {
			break
		}
	}
	if pick >= 0 {
		m.txs().focus.uid = m.blocks[pick].uid
	}
}

// revealBlock scrolls block bi into the window: all of it when it fits,
// else its first rows.
func (m *model) revealBlock(bi int) {
	if bi < 0 || bi >= len(m.blocks) {
		return
	}
	b := &m.blocks[bi]
	top, h := m.vp.YOffset, max(m.vp.Height, 1)
	switch {
	case b.y0 < top:
		m.vp.SetYOffset(b.y0)
	case b.y1 >= top+h:
		m.vp.SetYOffset(min(b.y0, b.y1-h+1))
	}
	m.follow = m.vp.AtBottom()
}

// revealRow scrolls transcript row y into the window, a third of the way
// down when it has to move.
func (m *model) revealRow(y int) {
	top, h := m.vp.YOffset, max(m.vp.Height, 1)
	if y < top || y >= top+h {
		m.vp.SetYOffset(y - h/3)
	}
	m.follow = m.vp.AtBottom()
}

// blockOpen reports whether b shows its open form: expanded for a kind with
// a toggle, unfolded for any other.
func blockOpen(b *block) bool {
	if hasToggle(b) {
		return b.expanded
	}
	return !b.folded
}

func hasToggle(b *block) bool {
	spec, ok := blockKinds[b.kind]
	return ok && spec.toggle != nil
}

// genericFoldable reports whether b, a kind without a toggle, has anything
// to fold: more than two rows.
func genericFoldable(b *block) bool {
	return !hasToggle(b) && b.kind != blockBanner && (b.folded || b.lines > 2)
}

// setOpen folds or opens b and reports whether anything changed.
func setOpen(b *block, open bool) bool {
	switch {
	case hasToggle(b):
		if b.expanded == open {
			return false
		}
		return b.toggle()
	case genericFoldable(b):
		if b.folded == !open {
			return false
		}
		b.folded = !open
		b.invalidate()
		return true
	}
	return false
}

// foldFocused folds or opens the focused block and keeps it on screen.
func (m *model) foldFocused(bi int, open bool) {
	if setOpen(&m.blocks[bi], open) {
		m.refreshVP()
		m.revealBlock(bi)
	}
}

// foldAll folds every block with a toggle when any is open, else opens
// them all. Blocks folded by hand stay as they are.
func (m *model) foldAll() {
	open := false
	for i := range m.blocks {
		if b := &m.blocks[i]; focusable(b) && hasToggle(b) && b.expanded {
			open = true
			break
		}
	}
	changed := false
	for i := range m.blocks {
		if b := &m.blocks[i]; focusable(b) && hasToggle(b) {
			changed = setOpen(b, !open) || changed
		}
	}
	if changed {
		m.refreshVP()
	}
}

// foldedRows is a folded block's rows: its first, then a count of the rest.
func foldedRows(rows []string) []string {
	if len(rows) <= 1 {
		return rows
	}
	n := len(rows) - 1
	label := fmt.Sprintf("  %s %d more lines", glyphs.Fold, n)
	if n == 1 {
		label = fmt.Sprintf("  %s 1 more line", glyphs.Fold)
	}
	return []string{rows[0], textHintStyle.Render(label)}
}

// findTypingKey edits the search query being typed.
func (m *model) findTypingKey(msg tea.KeyMsg) {
	f := m.txs().focus
	switch msg.Type {
	case tea.KeyEsc:
		f.typing = false
	case tea.KeyEnter:
		f.typing = false
		m.startFind(f.draft)
	case tea.KeyBackspace:
		if r := []rune(f.draft); len(r) > 0 {
			f.draft = string(r[:len(r)-1])
		}
	case tea.KeySpace:
		f.draft += " "
	case tea.KeyRunes:
		f.draft += string(msg.Runes)
	}
}

// startFind searches for q and jumps to the match nearest above the focused
// block's end: the search runs up the transcript, from the newest output.
func (m *model) startFind(q string) {
	f := m.txs().focus
	if strings.TrimSpace(q) == "" {
		f.find = nil
		return
	}
	f.find = &txFind{query: q, cur: -1}
	from := m.vp.TotalLineCount()
	if bi := m.focusIndex(); bi >= 0 {
		from = m.blocks[bi].y1 + 1
	}
	m.findJump(from, -1)
}

// findStep moves to the next match up (d < 0) or down the transcript,
// wrapping at the ends.
func (m *model) findStep(d int) {
	fd := m.txs().focus.find
	if fd == nil {
		return
	}
	from := -1
	if fd.cur >= 0 && fd.cur < len(fd.hits) {
		from = m.hitRow(fd.hits[fd.cur])
	}
	if from < 0 {
		if bi := m.focusIndex(); bi >= 0 {
			from = m.blocks[bi].y0
		}
	}
	m.findJump(from, d)
}

// findJump searches again and jumps to the first match past row from in
// direction d, wrapping around.
func (m *model) findJump(from, d int) {
	fd := m.txs().focus.find
	fd.hits = m.findHits(fd.query)
	fd.cur = -1
	if len(fd.hits) == 0 {
		return
	}
	pick := -1
	if d < 0 {
		for i := len(fd.hits) - 1; i >= 0; i-- {
			if m.hitRow(fd.hits[i]) < from {
				pick = i
				break
			}
		}
		if pick < 0 {
			pick = len(fd.hits) - 1
		}
	} else {
		for i, h := range fd.hits {
			if m.hitRow(h) > from {
				pick = i
				break
			}
		}
		if pick < 0 {
			pick = 0
		}
	}
	m.gotoHit(pick)
}

// gotoHit focuses match i's block and scrolls its row into view. A match in
// a folded block's hidden text opens the block, then lands on its first
// visible match.
func (m *model) gotoHit(i int) {
	fd := m.txs().focus.find
	h := fd.hits[i]
	bi := m.blockByUID(h.uid)
	if bi < 0 {
		return
	}
	if h.row < 0 {
		setOpen(&m.blocks[bi], true)
		m.refreshVP()
		fd.hits = m.findHits(fd.query)
		i = -1
		for j, o := range fd.hits {
			if o.uid == h.uid {
				i = j
				break
			}
		}
		if i < 0 {
			return
		}
		h = fd.hits[i]
	}
	fd.cur = i
	m.txs().focus.uid = h.uid
	m.revealRow(m.blocks[bi].y0 + max(h.row, 0))
}

func (m *model) blockByUID(uid uint64) int {
	for i := range m.blocks {
		if m.blocks[i].uid == uid {
			return i
		}
	}
	return -1
}

// hitRow is the transcript row a match sits on, or -1 once its block has gone.
func (m *model) hitRow(h txHit) int {
	bi := m.blockByUID(h.uid)
	if bi < 0 || m.blocks[bi].y0 < 0 {
		return -1
	}
	return m.blocks[bi].y0 + max(h.row, 0)
}

// findHits is every row q matches, top to bottom, plus one match for each
// folded block whose hidden text matches.
func (m *model) findHits(q string) []txHit {
	match := matcher(q)
	var hits []txHit
	for i := range m.blocks {
		b := &m.blocks[i]
		if !focusable(b) {
			continue
		}
		found := false
		for r := range b.lines {
			if match(b.plainRow(r)) {
				hits = append(hits, txHit{uid: b.uid, row: r})
				found = true
			}
		}
		if !found && !blockOpen(b) && match(ansi.Strip(b.text)) {
			hits = append(hits, txHit{uid: b.uid, row: -1})
		}
	}
	return hits
}

// smartCase reports whether q matches case-insensitively: it has no capital.
func smartCase(q string) bool {
	return !strings.ContainsFunc(q, unicode.IsUpper)
}

func matcher(q string) func(string) bool {
	if smartCase(q) {
		lq := strings.ToLower(q)
		return func(s string) bool { return strings.Contains(strings.ToLower(s), lq) }
	}
	return func(s string) bool { return strings.Contains(s, q) }
}

// matchSpans is where q occurs in plain, as [start, end) cell ranges.
func matchSpans(plain, q string) [][2]int {
	if q == "" {
		return nil
	}
	hay, needle := plain, q
	if smartCase(q) {
		hay, needle = strings.ToLower(plain), strings.ToLower(q)
		if len(hay) != len(plain) {
			// A case change that resizes the text would misplace the spans.
			hay, needle = plain, q
		}
	}
	var spans [][2]int
	for off := 0; off < len(hay); {
		i := strings.Index(hay[off:], needle)
		if i < 0 {
			break
		}
		s := off + i
		e := s + len(needle)
		spans = append(spans, [2]int{ansi.StringWidth(plain[:s]), ansi.StringWidth(plain[:e])})
		off = e
	}
	return spans
}

// highlightMatches shows every match of q on a drawn row in reverse video.
func highlightMatches(line, q string) string {
	for _, sp := range matchSpans(ansi.Strip(line), q) {
		line = reverseRange(line, sp[0], sp[1])
	}
	return line
}

// focusMark is the rail drawn down the focused block's left column.
func focusMark() string {
	if asciiMode {
		return "|"
	}
	return "┃"
}

// markFocus draws the focus rail and the search matches on the viewport's
// rows, lines[0] being transcript row vp.YOffset.
func (m *model) markFocus(lines []string) []string {
	if m.tx == nil || m.tx.focus == nil {
		return lines
	}
	f := m.tx.focus
	top := m.vp.YOffset
	if f.find != nil {
		for i, l := range lines {
			lines[i] = highlightMatches(l, f.find.query)
		}
	}
	bi := m.focusIndex()
	if bi < 0 {
		return lines
	}
	b := &m.blocks[bi]
	for y := max(b.y0, top); y <= b.y1 && y-top < len(lines); y++ {
		lines[y-top] = markRow(lines[y-top])
	}
	return lines
}

// markRow puts the focus rail in a row's first cell: over a blank cell, or
// as reverse video on a glyph there.
func markRow(line string) string {
	plain := ansi.Strip(line)
	if plain == "" || plain[0] == ' ' {
		return accentStyle.Render(focusMark()) + ansi.TruncateLeft(line, 1, "")
	}
	return reverseRange(line, 0, 1)
}

// blockLabel names a block's kind for the focus bar and the viewer.
func blockLabel(b *block) string {
	switch b.kind {
	case blockUser:
		return "prompt"
	case blockAssistant:
		return "reply"
	case blockThinking:
		return "thinking"
	case blockToolRun, blockToolQueued:
		return "tool call"
	case blockTool, blockHostedSearch:
		return "tool output"
	}
	return "note"
}

// transcriptBarRows is the focus bar's height, its separator included.
func (m *model) transcriptBarRows() int {
	if m.tx == nil || m.tx.focus == nil || m.tx.viewer != nil {
		return 0
	}
	return 2
}

// transcriptBarView is the focus bar above the composer, with the newline
// before and after it, or "" while the transcript does not have focus.
func (m *model) transcriptBarView() string {
	if m.transcriptBarRows() == 0 {
		return ""
	}
	f := m.tx.focus
	var left, right string
	switch {
	case f.typing:
		left = accentStyle.Render("/") + " " + f.draft + accentStyle.Render(chromeGlyphs("▏"))
		right = m.tr("Enter search · Esc cancel")
	default:
		n, at := 0, 0
		bi := m.focusIndex()
		for i := range m.blocks {
			if focusable(&m.blocks[i]) {
				n++
				if i == bi {
					at = n
				}
			}
		}
		left = accentStyle.Render(focusMark()+" "+m.tr("transcript")) + dimStyle.Render(fmt.Sprintf(" %d/%d", at, n))
		if bi >= 0 {
			left += dimStyle.Render(chromeGlyphs(" · ") + m.tr(blockLabel(&m.blocks[bi])))
		}
		right = m.tr("j/k move · e fold · E all · v view · / find · Esc back")
		if fd := f.find; fd != nil {
			state := m.tr("no match")
			if len(fd.hits) > 0 {
				state = fmt.Sprintf("%d/%d", fd.cur+1, len(fd.hits))
			}
			left += dimStyle.Render(chromeGlyphs(" · ")) + accentStyle.Render(fmt.Sprintf("%q", fd.query)) + dimStyle.Render(" "+state)
			right = m.tr("n/N older/newer · / new search · Esc back")
		}
	}
	return "\n" + barColumns(left, dimStyle.Render(chromeGlyphs(right)), m.width) + "\n"
}

// barColumns sets left and right on one row of width cells. When the two do
// not fit the right side is cut, and left out once too little of it would
// show.
func barColumns(left, right string, width int) string {
	width = max(width, 1)
	lw, rw := ansi.StringWidth(left), ansi.StringWidth(right)
	if lw+2+rw > width {
		room := width - lw - 2
		if room < 12 {
			return ansi.Truncate(left, width, "…")
		}
		right, rw = ansi.Truncate(right, room, "…"), room
	}
	return left + strings.Repeat(" ", width-lw-rw) + right
}

// findCommand is /find: search the transcript for its text, or start typing
// a query when it has none.
func (m *model) findCommand(q string) (tea.Model, tea.Cmd) {
	tx := m.txs()
	tx.viewer = nil
	if tx.focus == nil {
		if m.lastFocusable() < 0 {
			m.append(dimStyle.Render(m.tr("nothing to search yet")))
			return m, nil
		}
		m.focusTranscript()
	}
	if q == "" {
		tx.focus.typing, tx.focus.draft = true, ""
		return m, nil
	}
	m.startFind(q)
	return m, nil
}
