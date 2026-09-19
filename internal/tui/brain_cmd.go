package tui

import (
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/editor"
)

func (m *model) openBrain() tea.Cmd {
	path := config.BrainPath()
	if path == "" {
		m.append(errStyle.Render("/brain: cannot locate ~/.k-brain"))
		return nil
	}
	c, err := editor.Command(path)
	if err != nil {
		m.append(errStyle.Render("/brain: " + err.Error()))
		return nil
	}
	m.append(dimStyle.Render("editing " + path + " — save and quit to apply (next turn picks it up)"))
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return brainEditedMsg{path, err}
	})
}

type brainEditedMsg struct {
	path string
	err  error
}
