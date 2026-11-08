package tui

import "strings"

// keyContext says where a binding applies.
type keyContext string

const (
	// keyGlobal is the main screen with no dialog or overlay open.
	keyGlobal keyContext = "global"
	// keyComposer is the composer with the main screen showing.
	keyComposer keyContext = "composer"
	// keyBusy is while a turn runs.
	keyBusy keyContext = "busy"
	// keyIdle is while no turn runs and the composer is empty.
	keyIdle keyContext = "idle"
	// keyQueue is while a turn runs, messages are queued and the composer is empty.
	keyQueue keyContext = "queue"
	// keyMenu is while the completion menu is open.
	keyMenu keyContext = "menu"
	// keyDock is while the subagent dock has focus (Ctrl+T).
	keyDock keyContext = "dock"
	// keyTaskView is while a subagent's own view is open.
	keyTaskView keyContext = "taskview"
	// keyTranscript is while the transcript has focus (Alt+↑).
	keyTranscript keyContext = "transcript"
	// keyViewer is while the block viewer is open.
	keyViewer keyContext = "viewer"
)

// keyBinding is one row of the keymap.
type keyBinding struct {
	// key is the key as bubbletea names it (tea.KeyMsg.String()), except
	// that the space bar is "space".
	key     string
	context keyContext
	// action names what the key does there; rows sharing an action are one
	// behaviour reachable by several keys.
	action string
	// hint is the key's /help entry. Several rows are often described by one
	// entry, which then sits on the first of them and the rest leave hint empty.
	hint string
}

// keymap lists every key the TUI handles, by context. It is data only:
// dispatch is still written out in keys.go, composer_keys.go and tasks.go,
// and keymap_test.go checks that the two agree in both directions. /help
// prints the hints in this order. New rows go at the end of their group.
var keymap = []keyBinding{
	{"tab", keyComposer, "complete", "Tab — complete"},
	{"shift+tab", keyGlobal, "cycleMode", "Shift+Tab — cycle Normal / Plan / Auto review / Always allow"},
	{"ctrl+k", keyGlobal, "clear", "Ctrl+K — clear the conversation"},
	{"ctrl+t", keyGlobal, "dockToggle", "Ctrl+T — subagents dock: ↑/↓ select, Space preview, Enter open, Ctrl+X stop"},
	{"up", keyDock, "dockPrev", ""},
	{"down", keyDock, "dockNext", ""},
	{"space", keyDock, "dockPreview", ""},
	{"enter", keyDock, "dockOpen", ""},
	{"ctrl+x", keyDock, "dockStop", ""},
	{"enter", keyTaskView, "taskSend", "in a subagent view: Enter steers or follows up, Ctrl+X stops, PgUp/PgDn scroll, Esc returns"},
	{"ctrl+x", keyTaskView, "taskStop", ""},
	{"pgup", keyTaskView, "taskScroll", ""},
	{"pgdown", keyTaskView, "taskScroll", ""},
	{"up", keyTaskView, "taskScroll", ""},
	{"down", keyTaskView, "taskScroll", ""},
	{"ctrl+home", keyTaskView, "taskTop", ""},
	{"ctrl+end", keyTaskView, "taskBottom", ""},
	{"esc", keyTaskView, "taskClose", ""},
	{"ctrl+t", keyTaskView, "taskClose", ""},
	{"ctrl+o", keyGlobal, "toggleThinking", palHintThinking + " — toggle thinking tokens"},
	{"ctrl+e", keyGlobal, "expandTool", "Ctrl+E — expand the last tool result"},
	{"ctrl+j", keyComposer, "newline", "Ctrl+J / Shift+Enter — newline"},
	{"shift+enter", keyComposer, "newline", ""},
	{"alt+enter", keyComposer, "newline", ""},
	{"ctrl+g", keyGlobal, "editor", "Ctrl+G — edit the current draft in an external editor"},
	{"ctrl+v", keyGlobal, "pasteImage", "Ctrl+V — paste image"},
	{"esc", keyBusy, "interrupt", "Esc — interrupt the agent"},
	{"esc", keyIdle, "rewind", "Esc Esc (idle) — " + palDescRewind + " (↑/↓ browse, Enter rewinds, f forks)"},
	{"esc", keyComposer, "clearDraft", ""},
	{"up", keyQueue, "queuePrev", "while busy with queued messages: ↑/↓ select, Del removes"},
	{"down", keyQueue, "queueNext", ""},
	{"delete", keyQueue, "queueRemove", ""},
	{"backspace", keyQueue, "queueRemove", ""},
	{"pgup", keyGlobal, "scroll", "PgUp/PgDn — scroll · wheel — scroll · drag — select/copy text"},
	{"pgdown", keyGlobal, "scroll", ""},
	{"ctrl+c", keyGlobal, "quit", palHintQuit + " — quit"},
	{"ctrl+c", keyBusy, "interrupt", ""},

	// Not described in /help.
	{"enter", keyComposer, "submit", ""},
	{"up", keyComposer, "historyPrev", ""},
	{"down", keyComposer, "historyNext", ""},
	{"ctrl+n", keyComposer, "historyNext", ""},
	{"ctrl+p", keyGlobal, "palette", ""},
	{"alt+b", keyComposer, "wordLeft", ""},
	{"alt+left", keyComposer, "wordLeft", ""},
	{"tab", keyMenu, "menuNext", ""},
	{"shift+tab", keyMenu, "menuPrev", ""},
	{"down", keyMenu, "menuNext", ""},
	{"ctrl+n", keyMenu, "menuNext", ""},
	{"up", keyMenu, "menuPrev", ""},
	{"ctrl+p", keyMenu, "menuPrev", ""},
	{"enter", keyMenu, "menuAccept", ""},
	{"esc", keyMenu, "menuClose", ""},
	{"ctrl+z", keyComposer, "undo", "Ctrl+Z / Alt+Z — undo / redo the draft"},
	{"ctrl+_", keyComposer, "undo", ""},
	{"alt+z", keyComposer, "redo", ""},
	{"ctrl+w", keyComposer, "killWord", "Ctrl+W / Ctrl+U / Alt+K / Alt+D — cut word, to line start, to line end, next word · Ctrl+Y paste it back (Alt+Y older)"},
	{"alt+backspace", keyComposer, "killWord", ""},
	{"ctrl+u", keyComposer, "killLineStart", ""},
	{"alt+k", keyComposer, "killLineEnd", ""},
	{"alt+d", keyComposer, "killWordForward", ""},
	{"ctrl+y", keyComposer, "yank", ""},
	{"alt+y", keyComposer, "yankPop", ""},
	{"alt+m", keyComposer, "multiline", "Alt+M — multiline mode: Enter adds a line, Ctrl+J sends"},
	{"ctrl+r", keyComposer, "historySearch", "Ctrl+R — search prompt history (Tab: all directories)"},
	{"ctrl+s", keyComposer, "stash", "Ctrl+S — stash the draft, or restore it on an empty composer"},
	{"alt+a", keyComposer, "selectAll", "mouse in the composer: click moves the cursor, drag selects, double click a word, triple click / Alt+A all · with a selection: Backspace/Del deletes, typing or paste replaces, Ctrl+X cuts, Ctrl+C copies"},
	{"ctrl+x", keyComposer, "cutSelection", ""},
	{"left", keyComposer, "chipSkip", ""},
	{"right", keyComposer, "chipSkip", ""},
	{"enter", keyQueue, "queueSendNow", "with a queued message selected: Enter sends it now, Ctrl+E edits, Alt+↑/↓ moves"},
	{"ctrl+e", keyQueue, "queueEdit", ""},
	{"alt+up", keyQueue, "queueMove", ""},
	{"shift+up", keyQueue, "queueMove", ""},
	{"alt+down", keyQueue, "queueMove", ""},
	{"shift+down", keyQueue, "queueMove", ""},
	{"esc", keyQueue, "queueDeselect", ""},
	{"alt+e", keyBusy, "queueEditLast", "Alt+E (busy) — edit the last queued message · click a queued message to select it, click it again to edit, its ✗ removes it"},
	{"ctrl+b", keyBusy, "backgroundTool", "Ctrl+B (busy) — move the running shell command to the background; it keeps running and the agent is told when it exits"},
	{"f2", keyGlobal, "settings", ""},
	{"alt+up", keyGlobal, "focusTranscript", "Alt+↑ — focus the transcript: j/k select a block, h/l fold / open, e toggle, E fold all, v view, / find, Esc back"},
	{"down", keyTranscript, "focusNext", ""},
	{"j", keyTranscript, "focusNext", ""},
	{"up", keyTranscript, "focusPrev", ""},
	{"k", keyTranscript, "focusPrev", ""},
	{"home", keyTranscript, "focusFirst", ""},
	{"g", keyTranscript, "focusFirst", ""},
	{"end", keyTranscript, "focusLast", ""},
	{"G", keyTranscript, "focusLast", ""},
	{"pgup", keyTranscript, "scroll", ""},
	{"pgdown", keyTranscript, "scroll", ""},
	{"left", keyTranscript, "fold", ""},
	{"h", keyTranscript, "fold", ""},
	{"right", keyTranscript, "unfold", ""},
	{"l", keyTranscript, "unfold", ""},
	{"enter", keyTranscript, "toggleFold", ""},
	{"space", keyTranscript, "toggleFold", ""},
	{"e", keyTranscript, "toggleFold", ""},
	{"E", keyTranscript, "foldAll", ""},
	{"v", keyTranscript, "view", ""},
	{"/", keyTranscript, "find", ""},
	{"n", keyTranscript, "findOlder", ""},
	{"N", keyTranscript, "findNewer", ""},
	{"backspace", keyTranscript, "findEdit", ""},
	{"esc", keyTranscript, "blur", ""},
	{"q", keyTranscript, "blur", ""},
	{"esc", keyViewer, "viewerClose", "in the block viewer: j/k scroll, Space/PgDn page, g/G top / end, / find, n/N next / previous, Esc back"},
	{"q", keyViewer, "viewerClose", ""},
	{"down", keyViewer, "viewerScroll", ""},
	{"j", keyViewer, "viewerScroll", ""},
	{"up", keyViewer, "viewerScroll", ""},
	{"k", keyViewer, "viewerScroll", ""},
	{"pgdown", keyViewer, "viewerPage", ""},
	{"space", keyViewer, "viewerPage", ""},
	{"pgup", keyViewer, "viewerPage", ""},
	{"home", keyViewer, "viewerTop", ""},
	{"g", keyViewer, "viewerTop", ""},
	{"end", keyViewer, "viewerEnd", ""},
	{"G", keyViewer, "viewerEnd", ""},
	{"/", keyViewer, "viewerFind", ""},
	{"n", keyViewer, "viewerFindNext", ""},
	{"N", keyViewer, "viewerFindPrev", ""},
	{"enter", keyViewer, "viewerFind", ""},
	{"backspace", keyViewer, "viewerFind", ""},
}

// keymapHelp is /help's key section: every hint in keymap order, translated
// with tr and joined on one line.
func keymapHelp(tr func(string) string) string {
	var parts []string
	for _, b := range keymap {
		if b.hint != "" {
			parts = append(parts, tr(b.hint))
		}
	}
	return strings.Join(parts, " · ")
}
