package tui

import (
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"time"
)

const defaultPlaceholder = "Ask k-brain anything… (/ for commands, tab completes)"

func (m *model) applyAppearance() {
	invalidateMDRenderer()
	m.spin = spinner.New(spinner.WithSpinner(spinner.Dot))
	m.input.Prompt = "› "
	m.input.Placeholder = m.tr(defaultPlaceholder)
	m.input.FocusedStyle.Text = lipgloss.NewStyle()
	m.input.FocusedStyle.CursorLine = lipgloss.NewStyle()
	m.input.FocusedStyle.Placeholder = dimStyle
	m.input.BlurredStyle.Text = lipgloss.NewStyle()
	m.input.BlurredStyle.Placeholder = dimStyle
	m.input.Focus()
}

func viewportWindow(n, idx, budget int) (int, int) {
	if budget >= n {
		return 0, n
	}
	lo := max(idx-budget/2, 0)
	hi := min(lo+budget, n)
	return max(hi-budget, 0), hi
}

func (m *model) vpTopRows() int { return 2 }
func (m *model) vpXOff() int    { return 0 }

type noticeExpiredMsg uint64

func (m *model) showNotice(msg string) tea.Cmd {
	m.transientNotice = msg
	m.noticeGeneration++
	generation := m.noticeGeneration
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return noticeExpiredMsg(generation) })
}
