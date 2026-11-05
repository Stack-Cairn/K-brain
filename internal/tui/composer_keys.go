package tui

import (
	"regexp"
	"strings"

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
