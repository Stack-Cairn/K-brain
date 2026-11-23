package tui

import (
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
)

// keyNames holds every name bubbletea gives a non-text key ("up", "home",
// "ctrl+c", "shift+tab", "f2", ...), the strings key bindings compare
// tea.KeyMsg.String() against.
var keyNames = func() map[string]bool {
	names := map[string]bool{"space": true}
	for t := tea.KeyType(-256); t < 256; t++ {
		if t == tea.KeyRunes || t == tea.KeySpace {
			continue
		}
		if s := (tea.Key{Type: t}).String(); s != "" {
			names[s] = true
		}
	}
	return names
}()

// literalRun keeps typed text that happens to spell a key name from acting
// as that key. Text that arrives in one read — an input method committing a
// phrase, dictation, a laggy remote session — reaches the program as runs of
// runes split at spaces, so a word like "up", "home" or "delete" becomes a
// KeyRunes message whose String() is exactly "up": the composer's textarea,
// the filter inputs and every String()-matched binding would take it as the
// arrow key and the word would vanish. Such a run is marked as pasted, which
// is how bubbletea keeps text from matching bindings ("[up]"); it is still
// inserted as text everywhere text is accepted. A single key press is never
// a multi-rune run, and runs that spell no key name are left alone, so
// bursts of single-key commands (transcript focus, cards) keep working.
func literalRun(msg tea.KeyMsg) tea.KeyMsg {
	if msg.Type != tea.KeyRunes || msg.Paste || len(msg.Runes) < 2 {
		return msg
	}
	if spellsKeyName(msg.String()) {
		msg.Paste = true
	}
	return msg
}

// spellsKeyName reports whether s is what bubbletea would call a key press
// other than plain text: a key name, or a modifier chord such as "alt+x".
func spellsKeyName(s string) bool {
	if keyNames[s] {
		return true
	}
	if rest, ok := strings.CutPrefix(s, "alt+"); ok {
		return keyNames[rest] || utf8.RuneCountInString(rest) == 1
	}
	return false
}
