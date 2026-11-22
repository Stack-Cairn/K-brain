package tui

import "unicode"

// Keyboard selection in the composer: Shift with ←/→/↑/↓/Home/End extends a
// selection from where the cursor was, Alt+Shift+←/→ by a word. It is the
// same selection a drag or double click makes (composer_mouse.go), so typing
// replaces it, Backspace deletes it, Ctrl+X cuts and Ctrl+C copies it, and it
// is drawn the same way.

// selMove is how far a Shift key moves the selecting end.
type selMove int

const (
	selCharLeft selMove = iota
	selCharRight
	selLineUp
	selLineDown
	selLineStart
	selLineEnd
	selWordLeft
	selWordRight
)

// composerSelectMove extends (or starts) the draft selection by mv. The end
// that is not the cursor is the anchor: a selection made with the keyboard
// always has the cursor on its moving end.
func (m *model) composerSelectMove(mv selMove) {
	s := m.composerSnap()
	runes := []rune(s.val)
	off := min(s.off, len(runes))
	anchor := off
	if a, b, ok := m.composerSelection(); ok {
		switch off {
		case a:
			anchor = b
		case b:
			anchor = a
		}
	}
	to := off
	switch mv {
	case selCharLeft:
		to = max(off-1, 0)
	case selCharRight:
		to = min(off+1, len(runes))
	case selLineStart:
		for to > 0 && runes[to-1] != '\n' {
			to--
		}
	case selLineEnd:
		for to < len(runes) && runes[to] != '\n' {
			to++
		}
	case selWordLeft:
		to = wordLeftOf(runes, off)
	case selWordRight:
		to = wordRightOf(runes, off)
	case selLineUp, selLineDown:
		// The textarea knows its wrapped rows: let it move the cursor a row,
		// and past the first or last row go to the draft's start or end.
		if mv == selLineUp {
			if m.cursorOnFirstLine() {
				to = 0
			} else {
				m.input.CursorUp()
				to = m.cursorOffset(s.val)
			}
		} else {
			if m.cursorOnLastLine() {
				to = len(runes)
			} else {
				m.input.CursorDown()
				to = m.cursorOffset(s.val)
			}
		}
	}
	m.placeCursor(to)
	m.setComposerSel(min(anchor, to), max(anchor, to))
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' }

// wordLeftOf is the start of the word before off: back over anything that is
// not a word, then over the word.
func wordLeftOf(runes []rune, off int) int {
	for off > 0 && !isWordRune(runes[off-1]) {
		off--
	}
	for off > 0 && isWordRune(runes[off-1]) {
		off--
	}
	return off
}

// wordRightOf is the end of the word after off.
func wordRightOf(runes []rune, off int) int {
	for off < len(runes) && !isWordRune(runes[off]) {
		off++
	}
	for off < len(runes) && isWordRune(runes[off]) {
		off++
	}
	return off
}
