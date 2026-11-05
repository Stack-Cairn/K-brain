package tui

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

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

	if msg.Type == tea.KeyCtrlJ ||
		(msg.Type == tea.KeyEnter && msg.Alt) ||
		(msg.Type == tea.KeyRunes && msg.Alt && string(msg.Runes) == "\r") ||
		isShiftEnterSeq(msg) {
		return m.insertNewline()
	}

	if k := msg.String(); k == "alt+b" || k == "alt+left" {
		if m.wordLeftFromBlank() {
			return m, nil
		}
	}

	if m.taskVP != nil {
		return m.taskViewKey(msg)
	}

	if msg.Paste {
		if path, ok := pastedImagePath(string(msg.Runes)); ok {

			return m, func() tea.Msg { return pasteImageFileCmd(path) }
		}
	}

	if !msg.Paste && msg.Type == tea.KeyRunes && len(msg.Runes) > 1 {
		if path, ok := pastedImagePath(string(msg.Runes)); ok {
			return m, func() tea.Msg { return pasteImageFileCmd(path) }
		}
	}
	if msg.Paste && m.cfg != nil && m.cfg.CollapsePaste != nil && *m.cfg.CollapsePaste {
		if n := strings.Count(string(msg.Runes), "\n"); n >= 2 {
			m.pasteBuf = string(msg.Runes)
			m.input.SetValue(m.input.Value() + fmt.Sprintf("[Pasted ~%d lines]", n+1))
			m.input.CursorEnd()
			m.growInput()
			return m, nil
		}
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

	case tea.KeyTab:

		if m.menu != nil {
			m.menuCycle(1)
			return m, nil
		}
		m.openMenu()
		return m, nil

	case tea.KeyDown, tea.KeyCtrlN:
		if m.menu != nil {
			m.menu.idx = (m.menu.idx + 1) % len(m.menu.cands)
			return m, nil
		}
		if m.tasksFocus {
			m.taskSel = min(m.taskSel+1, len(m.dockTasks())-1)
			m.taskExpanded = false
			return m, nil
		}

		if m.busy && len(m.queue) > 0 && m.input.Value() == "" {
			if m.queueSel >= 0 {
				m.queueSel++
				if m.queueSel >= len(m.queue) {
					m.queueSel = -1
				}
			}
			return m, nil
		}

		if m.input.Value() == "" && len(m.dockTasks()) > 0 {
			m.tasksFocus = true
			m.taskSel = 0
			return m, nil
		}

		if !m.cursorOnLastLine() {
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		}
		m.histNext()
		return m, nil

	case tea.KeyShiftTab:
		if m.menu != nil {
			m.menuCycle(-1)
			return m, nil
		}
		m.cyclePermissionMode()
		return m, nil

	case tea.KeySpace:
		if m.tasksFocus && m.menu == nil {

			m.taskExpanded = !m.taskExpanded
			return m, nil
		}

		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.refreshMenu()
		return m, cmd

	case tea.KeyUp, tea.KeyCtrlP:
		if m.menu != nil {
			m.menu.idx = (m.menu.idx + len(m.menu.cands) - 1) % len(m.menu.cands)
			return m, nil
		}
		if m.tasksFocus {
			if m.taskSel == 0 {
				m.tasksFocus = false
				m.taskExpanded = false
				return m, nil
			}
			m.taskSel--
			m.taskExpanded = false
			return m, nil
		}

		if m.busy && len(m.queue) > 0 && m.input.Value() == "" &&
			(msg.Type == tea.KeyUp || msg.Type == tea.KeyShiftTab) {
			if m.queueSel < 0 {
				m.queueSel = len(m.queue) - 1
			} else if m.queueSel > 0 {
				m.queueSel--
			}
			return m, nil
		}

		if msg.Type == tea.KeyUp && !m.cursorOnFirstLine() {
			m.lastUp = m.nowFn()
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		}
		if msg.Type == tea.KeyUp && m.nowFn().Sub(m.lastUp) < 300*time.Millisecond {
			m.lastUp = m.nowFn()
			return m, nil
		}
		m.lastUp = m.nowFn()
		if msg.Type == tea.KeyCtrlP {
			m.openPalette()
			return m, nil
		}
		m.histPrev()
		return m, nil

	case tea.KeyDelete, tea.KeyBackspace:

		if m.busy && m.queueSel >= 0 && m.queueSel < len(m.queue) {
			m.queue = append(m.queue[:m.queueSel], m.queue[m.queueSel+1:]...)
			if m.queueSel >= len(m.queue) {
				m.queueSel = len(m.queue) - 1
			}
			if len(m.queue) == 0 {
				m.queueSel = -1
			}
			return m, nil
		}

		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.refreshMenu()
		return m, cmd

	case tea.KeyEnter:
		if m.namePrompt != nil {
			onOK := m.namePrompt.onOK
			value := strings.TrimSpace(m.input.Value())
			m.closeNamePrompt()
			onOK(value)
			return m, nil
		}
		if m.menu != nil {
			c := m.menu.cands[m.menu.idx]

			if m.menu.cyc && m.menu.head == "" && execNow[c.Text] {
				m.menu = nil
				m.input.Reset()
				return m.command(c.Text)
			}

			if m.menu.cyc {
				m.acceptPreview()
				return m, nil
			}

			if m.menu.head == "" && execNow[c.Text] {
				m.menu = nil
				m.input.Reset()
				return m.command(c.Text)
			}
			if m.accept() {
				return m, nil
			}

		}
		if m.tasksFocus {
			m.tasksFocus = false

			if tasks := m.dockList(); len(tasks) > 0 {
				m.openTask(tasks[min(m.taskSel, len(tasks)-1)].ID)
			}
			return m, nil
		}
		text := strings.TrimSpace(m.input.Value())

		if m.pasteBuf != "" {
			text = strings.Replace(text, strings.TrimSpace(fmt.Sprintf("[Pasted ~%d lines]", strings.Count(m.pasteBuf, "\n")+1)), strings.TrimSpace(m.pasteBuf), 1)
			m.pasteBuf = ""
		}
		if m.busy {
			switch {

			case text != "" && (busyCmd(text) || m.isPromptCommand(text)):
				if !strings.HasPrefix(text, "/auth ") {
					m.hist = append(m.hist, text)
					m.histIdx = len(m.hist)
				}
				m.input.Reset()
				m.menu = nil
				return m.command(text)
			case strings.HasPrefix(text, "!"):
				m.hist = append(m.hist, text)
				m.histIdx = len(m.hist)
				m.input.Reset()
				m.menu = nil
				m.runShell(text)
			case text != "" && m.agent.WaitingOnSubagents():

				m.hist = append(m.hist, text)
				m.histIdx = len(m.hist)
				m.input.Reset()
				m.menu = nil

				steerText := m.expandImageChips(text)
				if m.supportsVision() {
					if parts, note := imageParts(steerText); len(parts) > 0 {
						m.agent.SteerImages(steerText+note, parts)
					} else {
						m.agent.Steer(steerText)
					}
				} else {
					m.agent.Steer(steerText)
				}
				m.append(youStyle.Render("❯ ") + linkifyFilePaths(text, realFileExists) + dimStyle.Render("  (steered)"))
			case text != "":
				m.queue = append(m.queue, text)
				m.hist = append(m.hist, text)
				m.histIdx = len(m.hist)
				m.input.Reset()
				m.menu = nil
			case len(m.queue) > 0:

				if m.cancel != nil {
					m.cancel()
				}
			}
			return m, nil
		}
		if text == "" && len(m.queue) > 0 {

			return m.drainQueueHead()
		}
		if text == "" {
			return m, nil
		}
		m.input.Reset()
		m.menu = nil

		if !strings.HasPrefix(text, "/auth ") {
			m.hist = append(m.hist, text)
			m.histIdx = len(m.hist)
		}
		m.draft = ""
		if strings.HasPrefix(text, "/") {
			return m.command(text)
		}
		if strings.HasPrefix(text, "!") {
			m.runShell(text)
			return m, nil
		}
		return m.submit(text)
	}

	m.tasksFocus = false
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.refreshMenu()
	return m, cmd
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
