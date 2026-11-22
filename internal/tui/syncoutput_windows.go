package tui

// querySyncOutputTTY does not query the console; K_BRAIN_SYNC_OUTPUT=1 turns
// synchronized output on where the terminal supports it.
func querySyncOutputTTY() (supported, answered bool) { return false, false }
