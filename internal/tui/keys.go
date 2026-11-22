package tui

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// overlayOwnsKeys reports whether an open dialog or overlay takes key
// presses before the composer does (see key): the composer may still be on
// screen, but it is not where typing goes.
func (m *model) overlayOwnsKeys() bool {
	return m.iactive != nil || m.askDialog != nil || m.permDialog != nil || m.planOpen() || m.elicitOpen() ||
		m.palette != nil || m.rew != nil || m.picker != nil || m.mpicker != nil || m.surfaceOpen() || m.usageOpen()
}

// key routes a key press: an open dialog or overlay takes it first, then the
// composer's editing chords, then the global keys below; whatever is left is
// a composer key (composer_keys.go).
// helpKeyOpens reports whether a typed "?" opens /help instead of going into
// the composer: only on an empty composer that is not taking a name or a
// history search, and never from a paste.
func (m *model) helpKeyOpens(msg tea.KeyMsg) bool {
	return !msg.Paste && !msg.Alt && m.input.Value() == "" && m.namePrompt == nil &&
		(m.cmp == nil || m.cmp.hs == nil)
}

func (m *model) key(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// A "press again" notice lasts until its window closes or the next key;
	// a repeat of the same key shows it again below.
	m.clearArmNotice()
	if m.iactive != nil {
		return m.iactiveKey(msg)
	}
	// Ctrl+L redraws over any dialog or overlay; an interactive command above
	// gets it instead, as a shell would.
	if msg.Type == tea.KeyCtrlL {
		return m, repaint()
	}
	if m.askDialog != nil {
		m.askKey(msg)
		return m, nil
	}
	if m.permDialog != nil {
		m.permKey(msg)
		return m, nil
	}
	if m.planOpen() {
		m.planKey(msg)
		return m, nil
	}
	if m.elicitOpen() {
		m.elicitKey(msg)
		return m, nil
	}
	if m.palette != nil {
		return m.paletteKey(msg)
	}
	if m.rew != nil {
		return m.rewindKey(msg)
	}
	if m.picker != nil {
		return m.pickerKey(msg)
	}
	if m.mpicker != nil {
		return m.modelPickerKey(msg)
	}
	if m.surfaceOpen() {
		return m.surfaceKey(msg)
	}
	if r, cmd, ok := m.usageKey(msg); ok {
		return r, cmd
	}

	if r, cmd, ok := m.transcriptKey(msg); ok {
		return r, cmd
	}

	if r, cmd, ok := m.composerPreKey(msg); ok {
		return r, cmd
	}

	if m.taskVP != nil {
		return m.taskViewKey(msg)
	}
	if msg.String() == "f2" {
		return m, m.openSettings()
	}
	if k := msg.String(); k == "f1" || (k == "?" && m.helpKeyOpens(msg)) {
		return m.command("/help")
	}

	if r, cmd, ok := m.composerPasteKey(msg); ok {
		return r, cmd
	}

	if msg.String() == "alt+t" {
		m.toggleTopPanel()
		return m, nil
	}
	if msg.String() == "alt+o" {
		// Alt+O opens or folds the last tool result. Ctrl+E is the composer's
		// end of line and Ctrl+O the thinking toggle; Option+E is a dead key
		// on a Mac, Option+O a plain character the Option fallback can map.
		if i := m.expandTarget(); i >= 0 {
			m.blocks[i].toggle()
			m.refreshVP()
		}
		return m, nil
	}
	switch msg.Type {
	case tea.KeyCtrlG:
		return m, m.openPromptEditor()
	case tea.KeyCtrlX:
		if m.tasksFocus && len(m.dockTasks()) > 0 {
			m.stopSelectedTask()
			return m, nil
		}
	case tea.KeyCtrlB:
		// Without a backgroundable call it stays the composer's cursor left.
		if cmd, ok := m.backgroundTools(); ok {
			return m, cmd
		}
	case tea.KeyCtrlT:

		if len(m.dockTasks()) == 0 {
			return m, nil
		}
		m.tasksFocus = !m.tasksFocus
		m.taskExpanded = false
		m.clampTaskSel()
		return m, nil
	case tea.KeyCtrlC:
		if m.busy && m.cancel != nil {

			if !m.interrupt1 {
				m.interrupt1 = true
				return m, nil
			}
			m.cancel()
			return m, nil
		}

		if m.quit1 {
			m.quit1 = false
			return m, tea.Quit
		}
		m.quit1 = true
		m.showArmNotice(m.tr("Press Ctrl+C again to quit"))
		return m, tea.Tick(2*time.Second, func(time.Time) tea.Msg { return quitArmMsg{} })

	case tea.KeyPgUp, tea.KeyPgDown:
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		m.follow = m.vp.AtBottom()
		return m, cmd

	case tea.KeyEnd, tea.KeyCtrlEnd:
		// Scrolled up, End on an empty composer (or Ctrl+End at any time)
		// jumps back to the newest output; the footer says so. Otherwise
		// they stay the textarea's end of line and end of draft.
		if m.scrolledUp() && (msg.Type == tea.KeyCtrlEnd || m.input.Value() == "") {
			m.vp.GotoBottom()
			m.follow = true
			if msg.Type == tea.KeyEnd {
				return m, nil
			}
		}

	case tea.KeyEsc:

		if m.busy && m.cancel != nil && strings.TrimSpace(m.input.Value()) == "" {
			m.cancel()
			m.escGrace.mark()
			return m, nil
		}

		dismissed := true
		switch {
		case m.namePrompt != nil:
			masked := m.namePrompt.mask
			m.closeNamePrompt()
			if masked {
				m.escClr = false
				return m, nil
			}
		case m.menu != nil:
			if m.menu.cyc {
				m.input.SetValue(m.menu.base)
			}
			m.menu = nil
		case m.queueSel >= 0:
			m.queueSel = -1

		default:
			dismissed = false
		}
		if !dismissed {

			if strings.TrimSpace(m.input.Value()) != "" {
				if m.escClr {
					m.escClr = false
					m.hist = append(m.hist, strings.TrimSpace(m.input.Value()))
					m.histIdx = len(m.hist)
					m.input.Reset()
					return m, nil
				}
				m.escClr = true
				m.showArmNotice(m.tr("Esc again to clear the input (↑ recalls it)"))
				return m, tea.Tick(time.Second, func(time.Time) tea.Msg { return escArmMsg{} })
			}

			if m.escGrace.swallow() {
				return m, nil
			}
			if m.esc1 {
				m.esc1 = false
				m.openRewind()
				return m, nil
			}
			m.esc1 = true
			m.showArmNotice(m.tr("Esc again to rewind the conversation"))
			return m, tea.Tick(time.Second, func(time.Time) tea.Msg { return escArmMsg{} })
		}
		m.esc1 = false
		m.escClr = false
		return m, nil

	case tea.KeyCtrlV:

		return m, pasteImageCmd

	case tea.KeyCtrlO:

		m.toggleThinking()
		return m, nil

	case tea.KeyShiftTab:
		if m.menu != nil {
			m.menuCycle(-1)
			return m, nil
		}
		m.cyclePermissionMode()
		return m, nil
	}
	return m.composerKey(msg)
}

var csiURe = regexp.MustCompile(`^\?CSI\[([0-9 ]+)\]\?$`)

func csiUKey(rendered string) (tea.KeyMsg, bool) {
	mm := csiURe.FindStringSubmatch(rendered)
	if mm == nil {
		return tea.KeyMsg{}, false
	}
	var b []byte
	for f := range strings.FieldsSeq(mm[1]) {
		n, err := strconv.ParseUint(f, 10, 8)
		if err != nil {
			return tea.KeyMsg{}, false
		}
		b = append(b, byte(n))
	}
	if len(b) == 0 || b[len(b)-1] != 'u' {
		return tea.KeyMsg{}, false
	}
	codeStr, modStr, _ := strings.Cut(string(b[:len(b)-1]), ";")
	codeStr, _, _ = strings.Cut(codeStr, ":")
	code, err := strconv.Atoi(codeStr)
	if err != nil {
		return tea.KeyMsg{}, false
	}
	mods := 1
	if modStr != "" {
		modStr, _, _ = strings.Cut(modStr, ":")
		if mods, err = strconv.Atoi(modStr); err != nil {
			return tea.KeyMsg{}, false
		}
	}
	mods--
	shift, alt, ctrl := mods&1 != 0, mods&2 != 0, mods&4 != 0
	switch {
	case code == 27:
		return tea.KeyMsg{Type: tea.KeyEsc, Alt: alt}, true
	case code == 13 && !ctrl:
		return tea.KeyMsg{Type: tea.KeyEnter, Alt: alt}, true
	case code == 9 && !ctrl:
		if shift {
			return tea.KeyMsg{Type: tea.KeyShiftTab, Alt: alt}, true
		}
		return tea.KeyMsg{Type: tea.KeyTab, Alt: alt}, true
	case code == 127 && !ctrl:
		return tea.KeyMsg{Type: tea.KeyBackspace, Alt: alt}, true
	case code < 32 || code > 126:
		return tea.KeyMsg{}, false
	}
	r := rune(code)
	if ctrl {
		if r >= 'a' && r <= 'z' {
			r -= 'a' - 'A'
		}
		if r < '@' || r > '_' {
			return tea.KeyMsg{}, false
		}

		return tea.KeyMsg{Type: tea.KeyType(r & 0x1f), Alt: alt}, true
	}
	if shift && r >= 'a' && r <= 'z' {
		r -= 'a' - 'A'
	}
	if r == ' ' {
		return tea.KeyMsg{Type: tea.KeySpace, Alt: alt}, true
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}, Alt: alt}, true
}
