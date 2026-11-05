package tui

import "time"

// escGraceWindow is how long after an Esc that interrupted a turn further Esc
// presses are ignored. People often press Esc two or three times to stop a
// reply; without the window the extra presses would arm and then open rewind
// on a turn that has only just stopped.
const escGraceWindow = 1500 * time.Millisecond

// escGraceNow is the clock; tests replace it.
var escGraceNow = time.Now

// escGrace remembers when Esc last interrupted a turn.
type escGrace struct{ at time.Time }

// mark records that Esc just interrupted a turn.
func (g *escGrace) mark() { g.at = escGraceNow() }

// swallow reports whether an Esc now falls inside the window, which then
// slides so a run of presses is absorbed as one.
func (g *escGrace) swallow() bool {
	if g.at.IsZero() {
		return false
	}
	now := escGraceNow()
	if now.Sub(g.at) > escGraceWindow {
		g.at = time.Time{}
		return false
	}
	g.at = now
	return true
}
