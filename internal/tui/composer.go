package tui

import (
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
)

// composerState is the composer's own state, behind model.cmp: undo and redo,
// the kill ring, paste chips, the draft stash, multiline mode, the history
// search overlay, the queue editor and the slash-command MRU. It is created
// on first use, so models built by hand in tests work without it.
type composerState struct {
	undo, redo []composerSnap
	// before is the composer as the previous key found it, and beforeKind
	// what that key was; the next key compares against it to see whether the
	// previous one changed the draft.
	before     composerSnap
	beforeKind editKind
	primed     bool
	// runOpen says the newest undo entry starts a typing run that further
	// typing extends rather than adding entries of its own.
	runOpen bool

	kills    []string
	lastAct  composerAct
	prevAct  composerAct
	yankFrom int
	yankTo   int
	yankIdx  int

	pastes   map[int]string
	pasteSeq int

	stash []string

	multiline bool

	hs *historySearch

	queueEdit *queueEdit

	// sel is the selected part of the draft (a drag, double or triple
	// click, Alt+A); clicks counts clicks in a row on one spot.
	sel                *composerSel
	clickAt            time.Time
	clicks             int
	clickRow, clickCol int
	clickVal           string

	mru *slashMRU
}

// composer returns the composer state, creating it on first use.
func (m *model) composer() *composerState {
	if m.cmp == nil {
		m.cmp = &composerState{}
		if m.prog != nil {
			m.warmFileIndex()
		}
	}
	return m.cmp
}

// warmFileIndex starts the @file index build in the background and has each
// finished build refresh an open @ menu, so completion shows the files as
// soon as they are listed.
func (m *model) warmFileIndex() {
	post := m.poster()
	setFileIndexNotify(func() {
		post(viewProbe{fn: func(m *model) {
			val := m.input.Value()
			if strings.HasPrefix(val[strings.LastIndexAny(val, " \n")+1:], "@") {
				m.refreshMenu()
			}
		}})
	})
	go fileIndexSnapshot(0)
}

// composerSnap is the draft and the cursor in it, as a rune offset into
// Value() (rows joined with "\n").
type composerSnap struct {
	val string
	off int
}

func (m *model) composerSnap() composerSnap {
	val := m.input.Value()
	return composerSnap{val: val, off: m.cursorOffset(val)}
}

// cursorOffset is the cursor's rune offset into val, which must be the
// textarea's current value.
func (m *model) cursorOffset(val string) int {
	row := m.input.Line()
	off := 0
	for i, line := range strings.Split(val, "\n") {
		n := len([]rune(line))
		if i == row {
			li := m.input.LineInfo()
			return off + min(max(li.StartColumn+li.ColumnOffset, 0), n)
		}
		off += n + 1
	}
	return len([]rune(val))
}

// setComposer replaces the draft and puts the cursor at rune offset off.
func (m *model) setComposer(val string, off int) {
	m.input.SetValue(val)
	// growInput may rebuild the textarea with the cursor at the end, so it
	// runs before the cursor is placed.
	m.growInput()
	m.placeCursor(off)
}

// placeCursor puts the cursor at rune offset off into the current draft.
func (m *model) placeCursor(off int) {
	runes := []rune(m.input.Value()) // the textarea may have sanitized it
	off = min(max(off, 0), len(runes))
	prefix := string(runes[:off])
	row := strings.Count(prefix, "\n")
	col := len([]rune(prefix[strings.LastIndex(prefix, "\n")+1:]))
	for guard := 0; m.input.Line() > row && guard < 100000; guard++ {
		m.input.CursorUp()
	}
	for guard := 0; m.input.Line() < row && guard < 100000; guard++ {
		m.input.CursorDown()
	}
	m.input.SetCursor(col)
}

func (m *model) restoreSnap(s composerSnap) { m.setComposer(s.val, s.off) }

// editKind classifies a key for undo grouping.
type editKind int

const (
	editOther editKind = iota
	editTyping
	editSpace
	editSubmit
	editEsc
)

func keyEditKind(msg tea.KeyMsg) editKind {
	switch {
	case msg.Type == tea.KeyRunes && !msg.Alt && !msg.Paste:
		if len(msg.Runes) == 1 && unicode.IsSpace(msg.Runes[0]) {
			return editSpace
		}
		if len(msg.Runes) == 1 {
			return editTyping
		}
	case msg.Type == tea.KeySpace:
		return editSpace
	case msg.Type == tea.KeyEnter && !msg.Alt:
		return editSubmit
	case msg.Type == tea.KeyEsc:
		return editEsc
	}
	return editOther
}

// composerAct is what the previous composer key did, for kills that append
// to the last kill and for Alt+Y after a yank.
type composerAct int

const (
	actNone composerAct = iota
	actKill
	actYank
)

const maxUndo = 200

// observe runs at the start of every key the composer sees. It compares the
// draft with how the previous key found it and, when that key changed it,
// records the old draft for undo. Changes that did not come from a key (an
// image chip landing, a command clearing the input) are folded into the
// previous key's entry.
func (c *composerState) observe(m *model, msg tea.KeyMsg) {
	c.prevAct, c.lastAct = c.lastAct, actNone
	cur := m.composerSnap()
	if c.primed && cur.val != c.before.val {
		c.recordChange(c.before, c.beforeKind, cur)
	}
	c.before, c.beforeKind, c.primed = cur, keyEditKind(msg), true
}

func (c *composerState) recordChange(prev composerSnap, kind editKind, cur composerSnap) {
	switch {
	case kind == editSubmit && strings.TrimSpace(cur.val) == "":
		// Sent: the next draft starts a fresh history.
		c.undo, c.redo, c.runOpen = nil, nil, false
		c.dropUnusedPastes(cur.val)
		return
	case kind == editEsc && strings.TrimSpace(cur.val) == "" && strings.TrimSpace(prev.val) != "":
		// Esc Esc cleared the draft: keep it in the stash as well.
		c.pushStash(prev.val)
	}
	c.redo = nil
	if c.runOpen && (kind == editTyping || kind == editSpace) {
		c.runOpen = kind == editTyping
		return
	}
	c.pushUndo(prev)
	c.runOpen = kind == editTyping
}

func (c *composerState) pushUndo(s composerSnap) {
	c.undo = append(c.undo, s)
	if len(c.undo) > maxUndo {
		c.undo = c.undo[len(c.undo)-maxUndo:]
	}
}

// resync makes the current draft the baseline, so a change the composer made
// itself is not recorded again by the next key.
func (c *composerState) resync(m *model) {
	c.before = m.composerSnap()
	c.runOpen = false
}

func (m *model) composerUndo() {
	c := m.composer()
	if len(c.undo) == 0 {
		return
	}
	c.redo = append(c.redo, m.composerSnap())
	s := c.undo[len(c.undo)-1]
	c.undo = c.undo[:len(c.undo)-1]
	m.restoreSnap(s)
	c.resync(m)
	m.refreshMenu()
}

func (m *model) composerRedo() {
	c := m.composer()
	if len(c.redo) == 0 {
		return
	}
	c.pushUndo(m.composerSnap())
	s := c.redo[len(c.redo)-1]
	c.redo = c.redo[:len(c.redo)-1]
	m.restoreSnap(s)
	c.resync(m)
	m.refreshMenu()
}

// killUnit is how far a kill reaches from the cursor.
type killUnit int

const (
	killToLineStart killUnit = iota
	killToLineEnd
	killWordBack      // back to the previous whitespace (Ctrl+W)
	killAlnumWordBack // back over one word of letters and digits (Alt+Backspace)
	killAlnumWordFwd  // forward over one word of letters and digits (Alt+D)
)

const maxKills = 16

// killRange is the [a, b) rune range a kill removes from runes at off.
func killRange(runes []rune, off int, unit killUnit) (int, int) {
	a, b := off, off
	isWord := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' }
	switch unit {
	case killToLineStart:
		for a > 0 && runes[a-1] != '\n' {
			a--
		}
		if a == off && a > 0 {
			a--
		}
	case killToLineEnd:
		for b < len(runes) && runes[b] != '\n' {
			b++
		}
		if b == off && b < len(runes) {
			b++
		}
	case killWordBack:
		for a > 0 && (runes[a-1] == ' ' || runes[a-1] == '\t') {
			a--
		}
		if a > 0 && runes[a-1] == '\n' {
			if a == off {
				a--
			}
			break
		}
		for a > 0 && !unicode.IsSpace(runes[a-1]) {
			a--
		}
	case killAlnumWordBack:
		for a > 0 && !isWord(runes[a-1]) && runes[a-1] != '\n' {
			a--
		}
		if a == off && a > 0 && runes[a-1] == '\n' {
			a--
			break
		}
		for a > 0 && isWord(runes[a-1]) {
			a--
		}
	case killAlnumWordFwd:
		for b < len(runes) && !isWord(runes[b]) && runes[b] != '\n' {
			b++
		}
		if b == off && b < len(runes) && runes[b] == '\n' {
			b++
			break
		}
		for b < len(runes) && isWord(runes[b]) {
			b++
		}
	}
	return a, b
}

// composerKill removes text around the cursor into the kill ring. Kills in a
// row join into one ring entry, as in Emacs, so Ctrl+Y brings back all of it.
// A kill never leaves half a chip behind: it widens to cover any chip it cuts.
func (m *model) composerKill(unit killUnit) {
	c := m.composer()
	s := m.composerSnap()
	runes := []rune(s.val)
	a, b := killRange(runes, s.off, unit)
	a, b = widenToChips(runes, a, b)
	if a == b {
		c.lastAct = actKill
		return
	}
	killed := string(runes[a:b])
	backward := unit != killToLineEnd && unit != killAlnumWordFwd
	switch {
	case c.prevAct == actKill && len(c.kills) > 0 && backward:
		c.kills[len(c.kills)-1] = killed + c.kills[len(c.kills)-1]
	case c.prevAct == actKill && len(c.kills) > 0:
		c.kills[len(c.kills)-1] += killed
	default:
		c.kills = append(c.kills, killed)
		if len(c.kills) > maxKills {
			c.kills = c.kills[1:]
		}
	}
	c.lastAct = actKill
	m.setComposer(string(runes[:a])+string(runes[b:]), a)
	m.refreshMenu()
}

// composerYank inserts the newest kill at the cursor.
func (m *model) composerYank() {
	c := m.composer()
	if len(c.kills) == 0 {
		return
	}
	c.yankIdx = len(c.kills) - 1
	m.yankInsert(c.kills[c.yankIdx])
}

// composerYankPop replaces the text the last yank inserted with the kill
// before it, cycling through the ring. It only works straight after a yank.
func (m *model) composerYankPop() {
	c := m.composer()
	if c.prevAct != actYank || len(c.kills) == 0 {
		return
	}
	s := m.composerSnap()
	runes := []rune(s.val)
	if c.yankFrom < 0 || c.yankTo > len(runes) || c.yankFrom > c.yankTo {
		return
	}
	c.yankIdx = (c.yankIdx - 1 + len(c.kills)) % len(c.kills)
	m.setComposer(string(runes[:c.yankFrom])+string(runes[c.yankTo:]), c.yankFrom)
	m.yankInsert(c.kills[c.yankIdx])
}

func (m *model) yankInsert(text string) {
	c := m.composer()
	s := m.composerSnap()
	runes := []rune(s.val)
	ins := []rune(text)
	val := string(runes[:s.off]) + text + string(runes[s.off:])
	c.yankFrom, c.yankTo = s.off, s.off+len(ins)
	m.setComposer(val, c.yankTo)
	c.lastAct = actYank
	m.refreshMenu()
}

// composerDeleteChip makes chips atomic under Backspace and Delete: when the
// character the key would remove belongs to a chip, the whole chip goes.
func (m *model) composerDeleteChip(forward bool) bool {
	s := m.composerSnap()
	runes := []rune(s.val)
	for _, sp := range chipSpans(runes) {
		hit := s.off > sp[0] && s.off <= sp[1]
		if forward {
			hit = s.off >= sp[0] && s.off < sp[1]
		}
		if hit {
			m.setComposer(string(runes[:sp[0]])+string(runes[sp[1]:]), sp[0])
			m.refreshMenu()
			return true
		}
	}
	return false
}

// composerSkipChip moves the cursor over a whole chip with ←/→.
func (m *model) composerSkipChip(forward bool) bool {
	s := m.composerSnap()
	runes := []rune(s.val)
	for _, sp := range chipSpans(runes) {
		if !forward && s.off > sp[0] && s.off <= sp[1] {
			m.placeCursor(sp[0])
			return true
		}
		if forward && s.off >= sp[0] && s.off < sp[1] {
			m.placeCursor(sp[1])
			return true
		}
	}
	return false
}

// toggleMultiline flips multiline mode, where Enter adds a line and the
// newline chords send instead.
func (m *model) toggleMultiline() tea.Cmd {
	c := m.composer()
	c.multiline = !c.multiline
	if c.multiline {
		return m.showNotice(m.tr("multiline on · Enter adds a line · Ctrl+J / Shift+Enter sends · Alt+M ends"))
	}
	return m.showNotice(m.tr("multiline off · Enter sends"))
}

// multiline reports whether multiline mode is on.
func (m *model) multiline() bool { return m.cmp != nil && m.cmp.multiline }

// pushStash keeps a draft for later; the same draft twice in a row is kept once.
func (c *composerState) pushStash(draft string) {
	if strings.TrimSpace(draft) == "" {
		return
	}
	if n := len(c.stash); n > 0 && c.stash[n-1] == draft {
		return
	}
	c.stash = append(c.stash, draft)
	if len(c.stash) > 20 {
		c.stash = c.stash[1:]
	}
}

// stashDraft is Ctrl+S: with a draft it puts the draft aside and clears the
// composer; on an empty composer it brings back the newest stashed draft.
func (m *model) stashDraft() tea.Cmd {
	c := m.composer()
	if strings.TrimSpace(m.input.Value()) != "" {
		c.pushStash(m.input.Value())
		m.input.Reset()
		m.menu = nil
		m.growInput()
		return m.showNotice(m.tr("draft stashed · Ctrl+S on an empty composer restores it"))
	}
	if len(c.stash) == 0 {
		return m.showNotice(m.tr("no stashed draft"))
	}
	d := c.stash[len(c.stash)-1]
	c.stash = c.stash[:len(c.stash)-1]
	m.setComposer(d, len([]rune(d)))
	m.refreshMenu()
	return m.showNotice(m.tr("draft restored"))
}

// composerPanel is what the composer shows under itself besides the
// completion menu — the history search overlay — with its leading newline,
// or "" when nothing is open.
func (m *model) composerPanel() string {
	if m.cmp == nil || m.cmp.hs == nil {
		return ""
	}
	return "\n" + m.historyPanelView()
}

// composerPanelRows is composerPanel's height, for the chrome section table.
func (m *model) composerPanelRows() int { return m.historyPanelRows() }

// composerInputView is the textarea's view without the zero-width marks that
// end chips: they take no cell, but some terminals and the ASCII mode would
// show them.
func (m *model) composerInputView() string {
	return strings.ReplaceAll(m.input.View(), chipSentinel, "")
}
