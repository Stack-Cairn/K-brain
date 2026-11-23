package tui

import (
	"slices"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
)

// The keyboard cheatsheet is generated from the keymap table, so a key added
// there shows up here without further work. /help opens it on an interactive
// screen; `/help text` and non-interactive runs keep the plain-text listing.

// helpContexts orders the keymap's contexts and names their sections.
var helpContexts = []struct {
	ctx  keyContext
	name string
}{
	{keyGlobal, "Anywhere"},
	{keyComposer, "Composer"},
	{keyIdle, "Idle, composer empty"},
	{keyBusy, "While a turn runs"},
	{keyQueue, "Queued messages"},
	{keyMenu, "Completion menu"},
	{keyDock, "Subagent dock (Ctrl+T)"},
	{keyTaskView, "Subagent view"},
	{keyTranscript, "Transcript focus (Alt+↑)"},
	{keyViewer, "Block viewer"},
	{keyVim, "Vim normal mode (/vim)"},
}

// helpActionDesc describes keymap actions whose rows carry no hint, or whose
// hint covers several keys at once.
var helpActionDesc = map[string]string{
	"complete":        "complete a command, path or mention",
	"cycleMode":       "cycle the permission mode",
	"dockToggle":      "focus the subagent dock",
	"dockPrev":        "previous subagent",
	"dockNext":        "next subagent",
	"dockPreview":     "preview the selected subagent",
	"dockOpen":        "open the selected subagent",
	"dockStop":        "stop the selected subagent",
	"taskSend":        "steer or follow up",
	"taskStop":        "stop the subagent",
	"taskScroll":      "scroll",
	"taskTop":         "jump to the top",
	"taskBottom":      "jump to the bottom",
	"taskClose":       "back to the conversation",
	"toggleThinking":  "show or hide thinking tokens",
	"expandTool":      "expand or collapse the last tool result",
	"newline":         "insert a newline",
	"editor":          "edit the draft in an external editor",
	"pasteImage":      "paste an image",
	"redraw":          "redraw the screen",
	"interrupt":       "interrupt the agent",
	"rewind":          "rewind to an earlier message",
	"clearDraft":      "clear the draft",
	"queuePrev":       "select the previous queued message",
	"queueNext":       "select the next queued message",
	"queueRemove":     "remove the selected queued message",
	"scroll":          "scroll the transcript",
	"jumpBottom":      "back to the newest output",
	"halfPage":        "scroll half a page",
	"quit":            "quit",
	"submit":          "send the message",
	"historyPrev":     "previous prompt from history",
	"historyNext":     "next prompt from history",
	"palette":         "open the command palette",
	"wordLeft":        "move one word left",
	"menuNext":        "next suggestion",
	"menuPrev":        "previous suggestion",
	"menuAccept":      "accept the suggestion",
	"menuClose":       "close the menu",
	"settings":        "open settings",
	"undo":            "undo the last draft edit",
	"redo":            "redo the draft edit",
	"killWord":        "cut the word before the cursor",
	"killLineStart":   "cut to the start of the line",
	"killLineEnd":     "cut to the end of the line",
	"killWordForward": "cut the next word",
	"lineEnd":         "move to the end of the line",
	"yank":            "paste the last cut text",
	"yankPop":         "swap in older cut text",
	"multiline":       "toggle multiline mode (Ctrl+J sends)",
	"historySearch":   "search prompt history",
	"stash":           "stash the draft, or restore it",
	"chipSkip":        "step over an attachment chip",
	"queueSendNow":    "send the queued message now",
	"queueEdit":       "edit the queued message",
	"queueMove":       "move the queued message",
	"queueDeselect":   "deselect the queued message",
	"selectAll":       "select the whole draft",
	"cutSelection":    "cut the selected text",
	"selectChar":      "extend the selection a character",
	"selectRow":       "extend the selection a row",
	"selectEdge":      "extend the selection to the line's start or end",
	"selectWord":      "extend the selection a word",
	"queueEditLast":   "edit the last queued message",
	"backgroundTool":  "move the running shell command to the background",
	"focusTranscript": "focus the transcript, a block at a time",
	"focusNext":       "next block",
	"focusPrev":       "previous block",
	"focusFirst":      "first block",
	"focusLast":       "last block",
	"fold":            "fold the block",
	"unfold":          "open the block",
	"toggleFold":      "fold or open the block",
	"foldAll":         "fold or open every tool output and thinking run",
	"view":            "open the block full screen",
	"openDiagram":     "open the reply's mermaid diagrams in the system viewer",
	"find":            "search the transcript",
	"findOlder":       "previous match, up the transcript",
	"findNewer":       "next match, down the transcript",
	"turnPrev":        "previous prompt",
	"turnNext":        "next prompt",
	"copyBlock":       "copy the block (a reply's markdown, or just the code)",
	"rawView":         "show a reply's raw markdown",
	"vimNormal":       "insert mode → normal mode; in normal mode the usual Esc",
	"vimMove":         "move: left, down, up, right (j/k past the ends walk history)",
	"vimWord":         "next word, back a word, end of word",
	"vimLineEdge":     "line start, first non-blank, line end",
	"vimDelete":       "delete: x a character, dd the line, dw a word, D to line end",
	"vimChange":       "change: cw the word, cc the line, C to line end",
	"vimYank":         "yy yanks the line",
	"vimPut":          "put the yank after the cursor (a line below it)",
	"vimUndo":         "undo",
	"vimInsert":       "insert: before / after the cursor, at line start / end",
	"vimOpenLine":     "open a line below / above and insert",
	"vimSend":         "send, back in insert mode",
	"findEdit":        "edit the search query",
	"blur":            "back to the composer",
	"viewerClose":     "back to the transcript",
	"viewerScroll":    "scroll a row",
	"viewerPage":      "scroll a page",
	"viewerTop":       "jump to the top",
	"viewerEnd":       "jump to the end",
	"viewerFind":      "search the block",
	"viewerFindNext":  "next match",
	"viewerFindPrev":  "previous match",
	"help":            "open this cheatsheet (? only on an empty composer)",
}

// helpKeyLabel overrides the generated key label of a (context, action)
// pair whose key is pressed more than once (a single Esc only arms clearing
// a draft; the second one clears it).
var helpKeyLabel = map[string]string{
	"idle/rewind":         "Esc Esc",
	"composer/clearDraft": "Esc Esc",
	"global/quit":         "Ctrl+C Ctrl+C",
	"busy/interrupt":      "Esc / Ctrl+C Ctrl+C",
}

// prettyKey turns a bubbletea key name into the label the cheatsheet shows.
func prettyKey(k string) string {
	special := map[string]string{
		"up": "↑", "down": "↓", "left": "←", "right": "→",
		"pgup": "PgUp", "pgdown": "PgDn", "esc": "Esc", "enter": "Enter",
		"space": "Space", "delete": "Del", "backspace": "Backspace", "tab": "Tab",
		"home": "Home", "end": "End",
	}
	parts := strings.Split(k, "+")
	for i, p := range parts {
		if s, ok := special[p]; ok {
			parts[i] = s
			continue
		}
		// A lone letter keeps its case: j and G are different keys. With a
		// modifier it is a chord name, Ctrl+K.
		if len(p) == 1 {
			if len(parts) > 1 {
				p = strings.ToUpper(p)
			}
			parts[i] = p
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, "+")
}

// humanizeAction spells a camelCase action as words: "historyPrev" →
// "history prev".
func humanizeAction(a string) string {
	var b strings.Builder
	for i, r := range a {
		if unicode.IsUpper(r) && i > 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// helpKeyRows is the cheatsheet's key tab: one row per (context, action),
// its keys joined, grouped by context in helpContexts order.
func helpKeyRows(m *model) []surfaceRow {
	type group struct {
		keys []string
		hint string
	}
	var rows []surfaceRow
	for _, c := range helpContexts {
		var order []string
		groups := map[string]*group{}
		for _, b := range keymap {
			if b.context != c.ctx {
				continue
			}
			g := groups[b.action]
			if g == nil {
				g = &group{}
				groups[b.action] = g
				order = append(order, b.action)
			}
			g.keys = append(g.keys, prettyKey(b.key))
			if g.hint == "" {
				g.hint = b.hint
			}
		}
		for _, action := range order {
			g := groups[action]
			label := strings.Join(g.keys, " / ")
			if l, ok := helpKeyLabel[string(c.ctx)+"/"+action]; ok {
				label = l
			}
			desc := helpActionDesc[action]
			if desc == "" {
				desc = g.hint
				if _, after, ok := strings.Cut(desc, " — "); ok {
					desc = after
				}
			}
			if desc == "" {
				desc = humanizeAction(action)
			}
			hint, section := g.hint, c.name
			rows = append(rows, surfaceRow{
				section: section,
				key:     label,
				desc:    desc,
				search:  action + " " + hint,
				preview: func(m *model) []string {
					out := []string{cardTitleStyle.Render(chromeGlyphs(label)) + "  " + textSecondaryStyle.Render(chromeGlyphs(m.tr(desc)))}
					if hint != "" {
						w, _ := m.surfaceSize()
						for _, l := range wrapPreview(chromeGlyphs(m.tr(hint)), w) {
							out = append(out, textHintStyle.Render(l))
						}
					}
					out = append(out, textMutedStyle.Render(m.tr("context")+": "+chromeGlyphs(m.tr(section))))
					return out
				},
			})
		}
	}
	return rows
}

// helpCommandRows is the cheatsheet's command tab: the slash registry by
// category. Enter puts the command in the composer.
func helpCommandRows(m *model) []surfaceRow {
	byCat := map[string][]registryEntry{}
	var cats []string
	for _, e := range slashRegistry() {
		c := e.Category
		if c == "" {
			c = "Other"
		}
		if _, ok := byCat[c]; !ok {
			cats = append(cats, c)
		}
		byCat[c] = append(byCat[c], e)
	}
	order := []string{"Session", "Agent", "App", "Other"}
	var sorted []string
	for _, c := range order {
		if _, ok := byCat[c]; ok {
			sorted = append(sorted, c)
		}
	}
	for _, c := range cats {
		if !slices.Contains(order, c) {
			sorted = append(sorted, c)
		}
	}
	var rows []surfaceRow
	for _, c := range sorted {
		for _, e := range byCat[c] {
			e := e
			rows = append(rows, surfaceRow{
				section: c,
				key:     e.Name,
				desc:    e.Hint,
				search:  e.Args + " " + e.Keybind,
				preview: func(m *model) []string {
					head := cardTitleStyle.Render(e.Name)
					if e.Args != "" {
						head += " " + accentFgStyle.Render(e.Args)
					}
					out := []string{head}
					w, _ := m.surfaceSize()
					for _, l := range wrapPreview(chromeGlyphs(m.tr(e.Hint)), w) {
						out = append(out, textSecondaryStyle.Render(l))
					}
					if e.Keybind != "" {
						out = append(out, textMutedStyle.Render(m.tr("key")+": "+prettyKeys(e.Keybind)))
					}
					return out
				},
				enter: func(m *model) tea.Cmd {
					m.closeSurface()
					m.input.SetValue(e.Name + " ")
					m.input.CursorEnd()
					m.refreshMenu()
					return nil
				},
			})
		}
	}
	return rows
}

// prettyKeys labels a space-separated chord sequence like "ctrl+c ctrl+c".
func prettyKeys(s string) string {
	f := strings.Fields(s)
	for i, k := range f {
		f[i] = prettyKey(k)
	}
	return strings.Join(f, " ")
}

// helpCommand is /help: the cheatsheet on an interactive screen, the plain
// listing otherwise or with `/help text`.
func (m *model) helpCommand(args []string) {
	if m.height <= 0 || (len(args) > 0 && args[0] == "text") {
		m.append(dimStyle.Render(helpTextFor(m.language())))
		return
	}
	m.openHelp(strings.Join(args, " "))
}

// openHelp opens the cheatsheet with filter pre-filled; a filter starting
// with "/" opens the command tab.
func (m *model) openHelp(filter string) {
	s := &surfaceModal{
		kind:   "help",
		title:  "Keyboard shortcuts",
		filter: filter,
		tabs: []surfaceTab{
			{name: "Keys", rows: helpKeyRows, hints: "↑/↓ select · type to filter · Esc close"},
			{name: "Commands", rows: helpCommandRows, hints: "↑/↓ select · Enter insert · Esc close"},
		},
	}
	if strings.HasPrefix(filter, "/") {
		s.tab = 1
	}
	m.openSurface(s)
}
