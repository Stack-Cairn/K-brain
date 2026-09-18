package tui

import (
	"context"
	"os"
	"os/exec"
	"runtime"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

func (m *model) openMe() tea.Cmd {
	path := config.MePath()
	if path == "" {
		m.append(errStyle.Render("/me: cannot locate ~/.k-brain"))
		return nil
	}
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "vi"
		if runtime.GOOS == "windows" {
			editor = "notepad.exe"
		}
	}
	m.append(dimStyle.Render("editing " + path + " — save and quit to apply (next turn picks it up)"))

	c := exec.CommandContext(context.Background(), editor, path)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return meEditedMsg{path, err}
	})
}

type meEditedMsg struct {
	path string
	err  error
}
