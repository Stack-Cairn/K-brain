package tui

// Vim mode for the composer.
//
// Opt-in (/vim, /settings; config "vim"), off by default. The composer
// starts in insert mode, where every key works as it does without vim mode.
// Esc in insert mode goes to normal mode, where printable keys are commands:
//
//	h j k l  w b e  0 ^ $      motions (k / j on the first / last line
//	                           walk the prompt history, as ↑ / ↓ do)
//	x  dd dw  D                delete
//	cw cc  C                   change (delete, then insert)
//	yy  p                      yank the line, put after the cursor / line
//	u                          undo
//	i a I A o O                back to insert mode
//
// Keys that are not printable (Enter, arrows, Ctrl and Alt chords, paste)
// keep their usual meaning in both modes. Esc in normal mode is the plain
// Esc: with an empty composer it interrupts a running turn, or arms rewind.
// So with vim mode on an empty composer interrupts on Esc Esc (insert →
// normal, then interrupt), and the busy row says so.

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// vimState is the composer's vim mode state.
type vimState struct {
	// normal is set in normal mode; insert mode otherwise.
	normal bool
	// pending is an operator (d, c, y) waiting for its motion.
	pending rune
	// reg is the unnamed register; regLine marks a whole-line yank.
	reg     string
	regLine bool
}

// vimEnabled reports whether vim mode is on.
func (m *model) vimEnabled() bool { return m.cfg != nil && m.cfg.Vim != nil && *m.cfg.Vim }

// vimNormal reports whether the composer is in vim normal mode.
func (m *model) vimNormal() bool { return m.vimEnabled() && m.cmp != nil && m.cmp.vim.normal }

// vimInsert reports whether vim mode is on and the composer in insert mode.
func (m *model) vimInsert() bool { return m.vimEnabled() && !m.vimNormal() }

// vimTag is the mode indicator for the info row, or "" with vim mode off.
func (m *model) vimTag() string {
	switch {
	case !m.vimEnabled():
		return ""
	case m.vimNormal():
		return accentStyle.Render("-- NORMAL --")
	}
	return dimStyle.Render("-- INSERT --")
}

// vimCommand is /vim [on|off]: it turns vim mode on or off, or flips it.
func (m *model) vimCommand(args []string) {
	on := !m.vimEnabled()
	if len(args) > 0 {
		switch strings.ToLower(args[0]) {
		case "on":
			on = true
		case "off":
			on = false
		case "toggle":
		default:
			m.append(errStyle.Render("usage: /vim [on|off]"))
			return
		}
	}
	m.setVim(on)
	if on {
		m.append(dimStyle.Render(m.tr("vim mode: on — Esc for normal mode, i to insert")))
	} else {
		m.append(dimStyle.Render(m.tr("vim mode: off")))
	}
}

// setVim turns vim mode on or off and saves the choice.
func (m *model) setVim(on bool) {
	if m.cfg == nil {
		return
	}
	m.cfg.Vim = &on
	m.composer().vim = vimState{}
	if err := m.cfg.Save(); err != nil {
		m.append(errStyle.Render("config save failed: " + err.Error()))
	}
}

// vimYields reports whether something above the draft owns the keys: a
// name prompt, the queue pane, a queued message being edited, or in insert
// mode a completion menu. Vim mode stays out of their way.
func (m *model) vimYields() bool {
	if m.menu != nil && !m.vimNormal() {
		return true
	}
	return m.namePrompt != nil || m.queueFocused() || m.editingQueued() != nil
}

// vimKey handles a key in vim mode. ok is false for a key it leaves to the
// composer's usual handling.
func (m *model) vimKey(msg tea.KeyMsg) (cmd tea.Cmd, ok bool) {
	if !m.vimEnabled() || m.vimYields() {
		return nil, false
	}
	v := &m.composer().vim
	if !v.normal {
		if msg.Type == tea.KeyEsc {
			// Insert → normal; the cursor steps back onto the last
			// character typed, as in vi.
			v.normal, v.pending = true, 0
			s := m.composerSnap()
			if s.off > 0 && []rune(s.val)[s.off-1] != '\n' {
				m.placeCursor(s.off - 1)
			}
			return nil, true
		}
		return nil, false
	}
	if msg.Type == tea.KeyEsc && v.pending != 0 {
		v.pending = 0
		return nil, true
	}
	if msg.Type == tea.KeyEnter && !msg.Alt {
		// Sending starts the next draft in insert mode.
		v.normal, v.pending = false, 0
		return nil, false
	}
	if msg.Type != tea.KeyRunes || msg.Alt || msg.Paste || len(msg.Runes) != 1 {
		if msg.Type == tea.KeySpace {
			m.vimMotion('l')
			return nil, true
		}
		return nil, false
	}
	r := msg.Runes[0]
	// Normal mode has no completion menu: an edit or undo that leaves a
	// slash command in the draft must not open one under the next key.
	defer func() { m.menu = nil }()
	if op := v.pending; op != 0 {
		v.pending = 0
		m.vimOperator(op, r)
		return nil, true
	}
	k := string(r)
	switch {
	case k == "h" || k == "j" || k == "k" || k == "l" || k == "w" || k == "b" || k == "e" || k == "0" || k == "^" || k == "$":
		m.vimMotion(r)
	case k == "d" || k == "c" || k == "y":
		v.pending = r
	case k == "x":
		s := m.composerSnap()
		runes := []rune(s.val)
		if _, le := lineBounds(runes, s.off); s.off < le {
			m.vimDelete(s.off, s.off+1, false)
		}
	case k == "D" || k == "C":
		s := m.composerSnap()
		_, le := lineBounds([]rune(s.val), s.off)
		m.vimDelete(s.off, le, r == 'C')
	case k == "p":
		m.vimPut()
	case k == "u":
		m.composerUndo()
		m.vimClamp()
	case k == "i" || k == "a" || k == "I" || k == "A" || k == "o" || k == "O":
		m.vimInsertAt(r)
	}
	// Any other printable key does nothing: normal mode never types.
	return nil, true
}

// lineBounds is the start of off's line and the offset of its newline (or
// the end of the draft).
func lineBounds(runes []rune, off int) (int, int) {
	off = min(max(off, 0), len(runes))
	ls := off
	for ls > 0 && runes[ls-1] != '\n' {
		ls--
	}
	le := off
	for le < len(runes) && runes[le] != '\n' {
		le++
	}
	return ls, le
}

// vimClamp keeps the normal-mode cursor on a character: never past the
// last one of its line.
func (m *model) vimClamp() {
	s := m.composerSnap()
	ls, le := lineBounds([]rune(s.val), s.off)
	if s.off >= le && le > ls {
		m.placeCursor(le - 1)
	}
}

// vimClass sorts runes for w, b and e: words, other non-blank runs, blanks.
func vimClass(r rune) int {
	switch {
	case r == ' ' || r == '\t' || r == '\n':
		return 0
	case isWordRune(r):
		return 1
	}
	return 2
}

// vimWordStart is where w lands from off: the start of the next word, or
// the end of the draft.
func vimWordStart(runes []rune, off int) int {
	i := off
	if i < len(runes) {
		if c := vimClass(runes[i]); c != 0 {
			for i < len(runes) && vimClass(runes[i]) == c {
				i++
			}
		}
	}
	for i < len(runes) && vimClass(runes[i]) == 0 {
		i++
	}
	return i
}

// vimWordEnd is where e lands from off: the last rune of this or the next
// word.
func vimWordEnd(runes []rune, off int) int {
	i := off + 1
	for i < len(runes) && vimClass(runes[i]) == 0 {
		i++
	}
	if i >= len(runes) {
		return max(len(runes)-1, 0)
	}
	c := vimClass(runes[i])
	for i+1 < len(runes) && vimClass(runes[i+1]) == c {
		i++
	}
	return i
}

// vimWordBack is where b lands from off: the start of this or the previous
// word.
func vimWordBack(runes []rune, off int) int {
	i := off - 1
	for i >= 0 && vimClass(runes[i]) == 0 {
		i--
	}
	if i < 0 {
		return 0
	}
	c := vimClass(runes[i])
	for i > 0 && vimClass(runes[i-1]) == c {
		i--
	}
	return i
}

// vimMotion moves the cursor.
func (m *model) vimMotion(r rune) {
	s := m.composerSnap()
	runes := []rune(s.val)
	ls, le := lineBounds(runes, s.off)
	to := s.off
	switch r {
	case 'h':
		to = max(s.off-1, ls)
	case 'l':
		to = min(s.off+1, max(le-1, ls))
	case 'j', 'k':
		col := s.off - ls
		if r == 'k' {
			if ls == 0 {
				m.histPrev()
				m.vimClamp()
				return
			}
			pls, ple := lineBounds(runes, ls-1)
			to = min(pls+col, max(ple-1, pls))
		} else {
			if le >= len(runes) {
				m.histNext()
				m.vimClamp()
				return
			}
			nls, nle := lineBounds(runes, le+1)
			to = min(nls+col, max(nle-1, nls))
		}
	case 'w':
		to = vimWordStart(runes, s.off)
	case 'b':
		to = vimWordBack(runes, s.off)
	case 'e':
		to = vimWordEnd(runes, s.off)
	case '0':
		to = ls
	case '^':
		to = ls
		for to < le && (runes[to] == ' ' || runes[to] == '\t') {
			to++
		}
	case '$':
		to = max(le-1, ls)
	}
	m.placeCursor(to)
	m.vimClamp()
}

// vimOperator applies d, c or y to the motion key r: dd cc yy take the
// line, dw cw the word. Any other key cancels the operator.
func (m *model) vimOperator(op, r rune) {
	s := m.composerSnap()
	runes := []rune(s.val)
	ls, le := lineBounds(runes, s.off)
	v := &m.composer().vim
	switch {
	case r == op && op == 'y':
		v.reg, v.regLine = string(runes[ls:le]), true
	case r == op && op == 'd':
		from, to := ls, le
		switch {
		case le < len(runes):
			to = le + 1 // the line and its newline
		case ls > 0:
			from = ls - 1 // the last line: the newline before it
		}
		v.reg, v.regLine = string(runes[ls:le]), true
		m.vimEdit(string(runes[:from])+string(runes[to:]), from)
		// The cursor lands on the next line's first non-blank.
		m.vimMotion('^')
	case r == op && op == 'c':
		v.reg, v.regLine = string(runes[ls:le]), true
		m.vimEdit(string(runes[:ls])+string(runes[le:]), ls)
		v.normal = false
	case r == 'w' && (op == 'd' || op == 'c'):
		end := vimWordStart(runes, s.off)
		if op == 'c' && s.off < len(runes) && vimClass(runes[s.off]) != 0 {
			// cw changes to the end of the word, not the blanks after it.
			end = vimWordEnd(runes, s.off) + 1
		}
		// A word motion stops at the end of the line.
		end = min(end, le)
		if end <= s.off {
			if op == 'c' {
				v.normal = false
			}
			return
		}
		m.vimDelete(s.off, end, op == 'c')
	}
}

// vimDelete cuts runes [from, to) into the register and, with insert set,
// goes to insert mode at from.
func (m *model) vimDelete(from, to int, insert bool) {
	s := m.composerSnap()
	runes := []rune(s.val)
	v := &m.composer().vim
	if from < to {
		v.reg, v.regLine = string(runes[from:to]), false
		m.vimEdit(string(runes[:from])+string(runes[to:]), from)
	}
	if insert {
		v.normal = false
		return
	}
	m.vimClamp()
}

// vimPut is p: a line yank goes in below the cursor's line, anything else
// after the cursor.
func (m *model) vimPut() {
	v := &m.composer().vim
	if v.reg == "" && !v.regLine {
		return
	}
	s := m.composerSnap()
	runes := []rune(s.val)
	if v.regLine {
		_, le := lineBounds(runes, s.off)
		m.vimEdit(string(runes[:le])+"\n"+v.reg+string(runes[le:]), le+1)
		return
	}
	at := s.off
	if ls, le := lineBounds(runes, s.off); le > ls {
		at = min(s.off+1, le)
	}
	m.vimEdit(string(runes[:at])+v.reg+string(runes[at:]), at+len([]rune(v.reg))-1)
	m.vimClamp()
}

// vimInsertAt goes back to insert mode where i a I A o O put the cursor.
func (m *model) vimInsertAt(r rune) {
	s := m.composerSnap()
	runes := []rune(s.val)
	ls, le := lineBounds(runes, s.off)
	m.composer().vim.normal = false
	switch r {
	case 'a':
		m.placeCursor(min(s.off+1, le))
	case 'I':
		at := ls
		for at < le && (runes[at] == ' ' || runes[at] == '\t') {
			at++
		}
		m.placeCursor(at)
	case 'A':
		m.placeCursor(le)
	case 'o':
		m.vimEdit(string(runes[:le])+"\n"+string(runes[le:]), le+1)
	case 'O':
		m.vimEdit(string(runes[:ls])+"\n"+string(runes[ls:]), ls)
	}
}

// vimEdit replaces the draft as one undo step and puts the cursor at off.
func (m *model) vimEdit(val string, off int) {
	c := m.composer()
	c.pushUndo(m.composerSnap())
	c.redo = nil
	m.clearComposerSel()
	m.setComposer(val, off)
	c.resync(m)
}
