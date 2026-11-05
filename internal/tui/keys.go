package tui

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// key routes a key press: an open dialog or overlay takes it first, then the
// composer's editing chords, then the global keys below; whatever is left is
// a composer key (composer_keys.go).
func (m *model) key(msg tea.KeyMsg) (tea.Model, tea.Cmd) {

	if m.iactive != nil {
		return m.iactiveKey(msg)
	}
	if m.askDialog != nil {
		m.askKey(msg)
		return m, nil
	}
	if m.permDialog != nil {
		m.permKey(msg)
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

	if r, cmd, ok := m.composerPreKey(msg); ok {
		return r, cmd
	}

	if m.taskVP != nil {
		return m.taskViewKey(msg)
	}
	if msg.String() == "f2" {
		return m, m.openSettings()
	}

	if r, cmd, ok := m.composerPasteKey(msg); ok {
		return r, cmd
	}

	switch msg.Type {
	case tea.KeyCtrlG:
		return m, m.openPromptEditor()
	case tea.KeyCtrlX:
		if m.tasksFocus && len(m.dockTasks()) > 0 {
			m.stopSelectedTask()
			return m, nil
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
		m.append(errStyle.Render("  · press Ctrl+C again to quit"))
		return m, tea.Tick(2*time.Second, func(time.Time) tea.Msg { return quitArmMsg{} })

	case tea.KeyPgUp, tea.KeyPgDown:
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		m.follow = m.vp.AtBottom()
		return m, cmd

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
				m.append(errStyle.Render("  · Esc again: clear the input (↑ recalls it)"))
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
			m.append(dimStyle.Render("  · Esc again: rewind the conversation"))
			return m, tea.Tick(time.Second, func(time.Time) tea.Msg { return escArmMsg{} })
		}
		m.esc1 = false
		m.escClr = false
		return m, nil

	case tea.KeyCtrlV:

		return m, pasteImageCmd

	case tea.KeyCtrlE:

		for i := len(m.blocks) - 1; i >= 0; i-- {
			if m.blocks[i].kind == blockTool {
				m.blocks[i].toggle()
				m.refreshVP()
				return m, nil
			}
		}
		m.tasksFocus = false
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.refreshMenu()
		return m, cmd

	case tea.KeyCtrlO:

		m.toggleThinking()
		return m, nil

	case tea.KeyCtrlK:

		return m.command("/clear")

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
