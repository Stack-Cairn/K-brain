package tui

import tea "github.com/charmbracelet/bubbletea"

// repaint rewrites every row of the screen on the next frame, without clearing
// it first. Between frames the renderer only rewrites the rows that changed, so
// a row the terminal shows wrong stays wrong for as long as kn draws the same
// thing there: after output kn did not write scrolled the screen, say, the rule
// over the composer can keep showing an old copy of the info row for the rest
// of the session. Ctrl+L asks for this, and kn does it itself when a turn ends
// and when the terminal regains focus.
//
// It asks the terminal for its size rather than reusing the last one, so a
// resize racing it cannot leave the frame laid out for the old size; the size
// that comes back makes the renderer forget its last frame.
func repaint() tea.Cmd { return tea.WindowSize() }
