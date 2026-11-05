package tui

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func newInput() textarea.Model {
	ti := textarea.New()
	ti.Placeholder = inputPlaceholder
	ti.Prompt = "❯ "
	ti.SetHeight(1)
	ti.MaxHeight = 24
	ti.ShowLineNumbers = false
	ti.KeyMap.InsertNewline = key.NewBinding(
		key.WithKeys("ctrl+j", "shift+enter", "alt+enter"),
		key.WithHelp("ctrl+j", "newline"),
	)

	ti.KeyMap.DeleteAfterCursor = key.NewBinding()

	ti.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ti.FocusedStyle.Placeholder = dimStyle
	ti.BlurredStyle.Placeholder = dimStyle
	ti.FocusedStyle.Prompt = accentStyle
	ti.BlurredStyle.Prompt = dimStyle
	// A blinking cursor repaints twice a second forever. bubbletea walks from
	// home with a run of newlines to reach the composer on each blink, which
	// some terminals — JetBrains' JediTerm among them — redraw as a visible
	// flicker. A static block still shows where typing lands.
	ti.Cursor.SetMode(cursor.CursorStatic)
	ti.Focus()
	return ti
}

func (m *model) inputContentHeight() int {
	contentWidth := max(

		m.input.Width()-2, 1,
	)
	h := 0
	for line := range strings.SplitSeq(m.input.Value(), "\n") {
		h += max(1, (lipgloss.Width(line)+contentWidth-1)/contentWidth)
	}
	return h
}

func (m *model) growInput() {
	if m.width <= 0 {
		return
	}
	h := max(1, min(m.inputContentHeight(), m.input.MaxHeight))
	if h == m.input.Height() {
		return
	}
	if h < m.input.Height() {
		m.input.SetHeight(h)
		return
	}
	val := m.input.Value()
	ti := newInput()

	ti.Prompt = m.input.Prompt
	ti.Placeholder = m.input.Placeholder
	ti.FocusedStyle = m.input.FocusedStyle
	ti.BlurredStyle = m.input.BlurredStyle
	ti.SetWidth(m.input.Width() + lipgloss.Width(ti.Prompt))
	ti.SetHeight(h)
	ti.SetValue(val)
	ti.CursorEnd()
	m.input = ti
	m.input.Focus()
}

var shiftEnterRe = regexp.MustCompile(
	`\[49 51 59 50 117\]` +
		`|\[50 55 59 50 59 49 51 126\]` +
		`|\[53 55 52 52 49 117\]` +
		`|'\[', '1', '3', ';', '2', 'u'` +
		`|'\[', '2', '7', ';', '2', ';', '1', '3', '~'` +
		`|'\[', 'five', 'seven', 'four', 'four', 'one', 'u'`,
)

func isShiftEnterSeq(msg tea.KeyMsg) bool {
	return isShiftEnterString(msg.String())
}

func isShiftEnterString(s string) bool {
	return (strings.HasPrefix(s, "?CSI[") || strings.HasPrefix(s, "unknown csi sequence:")) && shiftEnterRe.MatchString(s)
}

func (m *model) wordLeftFromBlank() bool {
	lines := strings.Split(m.input.Value(), "\n")
	row := m.input.Line()
	if row < 0 || row >= len(lines) {
		return false
	}
	li := m.input.LineInfo()
	col := min(li.StartColumn+li.CharOffset, len([]rune(lines[row])))
	if strings.TrimSpace(string([]rune(lines[row])[:col])) != "" {
		return false
	}
	if row == 0 {
		m.input.CursorStart()
	} else {
		m.input.CursorUp()
		m.input.CursorEnd()
	}
	return true
}

func (m *model) insertNewline() (tea.Model, tea.Cmd) {
	maxHeight := m.input.MaxHeight
	m.input.MaxHeight = 0
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	m.input.MaxHeight = maxHeight
	m.input.SetHeight(maxHeight)

	v := m.input.Value()
	m.input.SetValue(v)
	m.input.CursorEnd()
	m.refreshMenu()
	return m, cmd
}

func (m *model) histPrev() {
	if len(m.hist) == 0 || m.histIdx == 0 {
		return
	}
	if m.histIdx == len(m.hist) {
		m.draft = m.input.Value()
	}
	m.histIdx--
	m.input.SetValue(m.hist[m.histIdx])
}

func (m *model) histNext() {
	if m.histIdx >= len(m.hist) {
		return
	}
	m.histIdx++
	if m.histIdx == len(m.hist) {
		m.input.SetValue(m.draft)
	} else {
		m.input.SetValue(m.hist[m.histIdx])
	}
}

func (m *model) cursorOnFirstLine() bool {
	if m.input.Line() != 0 {
		return false
	}
	return m.input.LineInfo().RowOffset == 0
}

func (m *model) cursorOnLastLine() bool {
	if m.input.Line() != m.input.LineCount()-1 {
		return false
	}
	li := m.input.LineInfo()
	return li.RowOffset >= li.Height-1
}

const inputPlaceholder = "Ask k-brain anything…  / commands · Shift+Tab mode · Ctrl+P menu"

func (m *model) syncInputPlaceholder() {
	if m.input.Value() != "" {
		return
	}
	switch {
	case !m.busy:
		m.input.Placeholder = m.tr(inputPlaceholder)
	case m.agent != nil && m.agent.WaitingOnSubagents():
		m.input.Placeholder = m.tr("waiting on subagents — type to steer this turn")
	default:
		m.input.Placeholder = m.tr("busy — type to queue (sent when the turn ends)")
	}
	m.input.Placeholder = chromeGlyphs(m.input.Placeholder)
}

// composerPreKey handles the editing keys that act on the draft before any
// other routing: the newline chords and word-left from a blank prefix.
func (m *model) composerPreKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	if msg.Type == tea.KeyCtrlJ ||
		(msg.Type == tea.KeyEnter && msg.Alt) ||
		(msg.Type == tea.KeyRunes && msg.Alt && string(msg.Runes) == "\r") ||
		isShiftEnterSeq(msg) {
		r, cmd := m.insertNewline()
		return r, cmd, true
	}

	if k := msg.String(); k == "alt+b" || k == "alt+left" {
		if m.wordLeftFromBlank() {
			return m, nil, true
		}
	}
	return m, nil, false
}

// composerPasteKey turns a pasted image path into an attachment and, when
// configured, collapses a long paste into a placeholder.
func (m *model) composerPasteKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	if msg.Paste {
		if path, ok := pastedImagePath(string(msg.Runes)); ok {

			return m, func() tea.Msg { return pasteImageFileCmd(path) }, true
		}
	}

	if !msg.Paste && msg.Type == tea.KeyRunes && len(msg.Runes) > 1 {
		if path, ok := pastedImagePath(string(msg.Runes)); ok {
			return m, func() tea.Msg { return pasteImageFileCmd(path) }, true
		}
	}
	if msg.Paste && m.cfg != nil && m.cfg.CollapsePaste != nil && *m.cfg.CollapsePaste {
		if n := strings.Count(string(msg.Runes), "\n"); n >= 2 {
			m.pasteBuf = string(msg.Runes)
			m.input.SetValue(m.input.Value() + fmt.Sprintf("[Pasted ~%d lines]", n+1))
			m.input.CursorEnd()
			m.growInput()
			return m, nil, true
		}
	}
	return m, nil, false
}

// composerKey handles the keys that belong to the composer: completion,
// history and queue navigation, deletion and submit. Anything else is typed
// into the textarea.
func (m *model) composerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
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
