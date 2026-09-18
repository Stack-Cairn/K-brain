package tui

import (
	"fmt"
	"os"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/editor"
	tea "github.com/charmbracelet/bubbletea"
)

type promptEditedMsg struct {
	path     string
	original string
	err      error
}

func (m *model) openPromptEditor() tea.Cmd {
	if m.busy {
		m.append(dimStyle.Render("editor: wait for the current turn to finish"))
		return nil
	}
	original := m.input.Value()
	text := original
	if m.pasteBuf != "" {
		marker := fmt.Sprintf("[Pasted ~%d lines]", strings.Count(m.pasteBuf, "\n")+1)
		text = strings.Replace(text, marker, m.pasteBuf, 1)
	}
	cmd, path, err := editor.Prepare(text)
	if err != nil {
		m.append(errStyle.Render("editor: " + err.Error()))
		return nil
	}
	m.menu = nil
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return promptEditedMsg{path: path, original: original, err: err}
	})
}

func (m *model) applyPromptEdit(msg promptEditedMsg) {
	if msg.err != nil {
		m.append(errStyle.Render("editor: " + msg.err.Error() + " — draft preserved at " + msg.path))
		return
	}
	if m.input.Value() != msg.original {
		m.append(errStyle.Render("editor: input changed — edited draft preserved at " + msg.path))
		return
	}
	text, err := editor.Read(msg.path)
	if err != nil {
		m.append(errStyle.Render("editor: " + err.Error() + " — draft preserved at " + msg.path))
		return
	}
	m.input.SetValue(text)
	if m.input.Value() != text {
		m.input.SetValue(msg.original)
		m.append(errStyle.Render("editor: text exceeds input limits or contains unsupported controls — draft preserved at " + msg.path))
		return
	}
	m.pasteBuf = ""
	m.menu = nil
	m.input.CursorEnd()
	m.growInput()
	if err := os.Remove(msg.path); err != nil {
		m.append(errStyle.Render("editor: cannot remove temporary draft: " + err.Error()))
	}
	m.append(dimStyle.Render("draft updated — review and press Enter to send"))
}
