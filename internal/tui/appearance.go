package tui

import (
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/lipgloss"
)

const defaultPlaceholder = inputPlaceholder

func (m *model) applyAppearance() {
	invalidateMDRenderer()
	m.spin = spinner.New(spinner.WithSpinner(themeSpinner()), spinner.WithStyle(runningStyle))
	m.input.Prompt = glyphUser
	m.input.Placeholder = chromeGlyphs(m.tr(defaultPlaceholder))
	// Typed text is body text: the terminal's own foreground. Blurred, it steps
	// back to text.secondary.
	m.input.FocusedStyle.Text = lipgloss.NewStyle()
	m.input.FocusedStyle.Placeholder = dimStyle
	m.input.FocusedStyle.Prompt = youStyle
	m.input.BlurredStyle.Text = chromeStyle
	m.input.BlurredStyle.Placeholder = dimStyle
	m.input.BlurredStyle.Prompt = dimStyle
	m.input.Focus()
}

// themeSpinner is the busy spinner from the active glyph set, at about 7.5
// frames a second for braille and a little slower for the ASCII bar.
func themeSpinner() spinner.Spinner {
	fps := time.Second * 2 / 15
	if asciiMode {
		fps = time.Second / 6
	}
	return spinner.Spinner{Frames: glyphs.Spinner, FPS: fps}
}

func viewportWindow(n, idx, budget int) (int, int) {
	if budget >= n {
		return 0, n
	}
	lo := max(idx-budget/2, 0)
	hi := min(lo+budget, n)
	return max(hi-budget, 0), hi
}

// vpTopRows is the rows drawn above the transcript: the top panel.
func (m *model) vpTopRows() int { return m.tp.drawn }
func (m *model) vpXOff() int    { return 0 }
