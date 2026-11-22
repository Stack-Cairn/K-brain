package tui

// The macOS Option key.
//
// Alt shortcuts reach kn as Esc-prefixed keys only when the terminal sends
// Option as Meta. Out of the box Terminal and iTerm2 do not: Option+O types
// "ø", Option+T "†". kn cannot tell that "ø" from someone typing Danish, so
// it never reads these characters as shortcuts on its own:
//
//   - /doctor (on macOS) says how to make Option act as Meta, and the first
//     time such a character lands in an empty composer a one-line note
//     points there. The character is still typed.
//   - "Option as Alt" (/settings, config "optionAsAlt", off by default)
//     reads the US-layout Option+letter characters of kn's Alt shortcuts as
//     those shortcuts, for people who would rather not change the terminal.
//     With it on those characters cannot be typed.

import (
	"fmt"
	"runtime"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// optionLetters maps what Option+letter types on the US layout to the
// letter, for the letters kn binds to Alt (Alt+E is missing: Option+E is a
// dead key, so nothing arrives until the next key).
var optionLetters = map[rune]rune{
	'å': 'a', '∫': 'b', '∂': 'd', 'ƒ': 'f', '˚': 'k',
	'µ': 'm', 'ø': 'o', '†': 't', '¥': 'y', 'Ω': 'z',
}

// onMac reports whether kn runs on macOS; tests override it.
var onMac = runtime.GOOS == "darwin"

// optionAsAlt reports whether the Option-character fallback is on.
func (m *model) optionAsAlt() bool {
	return m.cfg != nil && m.cfg.OptionAsAlt != nil && *m.cfg.OptionAsAlt
}

// setOptionAsAlt turns the fallback on or off and saves it.
func (m *model) setOptionAsAlt(on bool) {
	if m.cfg == nil {
		return
	}
	m.cfg.OptionAsAlt = &on
	if err := m.cfg.Save(); err != nil {
		m.append(errStyle.Render("config save failed: " + err.Error()))
	}
}

// optionKey is msg as kn should read it: with the fallback on, an
// Option+letter character becomes Alt+letter. With it off, the first such
// character typed into an empty composer on macOS adds the /doctor note.
func (m *model) optionKey(msg tea.KeyMsg) tea.KeyMsg {
	if msg.Type != tea.KeyRunes || msg.Alt || msg.Paste || len(msg.Runes) != 1 {
		return msg
	}
	letter, ok := optionLetters[msg.Runes[0]]
	if !ok {
		return msg
	}
	if m.optionAsAlt() {
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{letter}, Alt: true}
	}
	if c := m.composer(); onMac && !c.optionHinted && m.input.Value() == "" {
		c.optionHinted = true
		note := fmt.Sprintf(m.tr("%s is what Option+%s types unless the terminal sends Option as Meta; for Alt shortcuts see /doctor"),
			string(msg.Runes[0]), strings.ToUpper(string(letter)))
		m.append(dimStyle.Render("◐ " + note))
	}
	return msg
}

// optionKeyDoctor is /doctor's line on the Option key, on macOS only.
func (m *model) optionKeyDoctor() string {
	if !onMac {
		return ""
	}
	if m.optionAsAlt() {
		return "  option key           Option as Alt is on: Option+A B D F K M O T Y Z act as Alt shortcuts (US layout)"
	}
	return "  option key           Alt shortcuts need Option to send Meta: Terminal › Settings › Profiles › Keyboard › " +
		"“Use Option as Meta key”; iTerm2 › Settings › Profiles › Keys › Left Option key: Esc+. " +
		"Or turn on “Option as Alt” in /settings (Option+letter characters then stop typing)"
}
