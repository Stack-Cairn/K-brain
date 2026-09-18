package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestCtrlJFirstLineStaysVisible(t *testing.T) {
	m := compactCmdModel()
	m.queueSel = -1
	p := tea.NewProgram(m, tea.WithOutput(nopWriter{}), tea.WithInput(strings.NewReader("")), tea.WithoutSignalHandler())
	done := make(chan struct{})
	go func() { p.Run(); close(done) }()
	defer func() { p.Kill(); <-done }()
	time.Sleep(100 * time.Millisecond)

	for _, r := range "hello first line" {
		p.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		time.Sleep(30 * time.Millisecond)
	}
	p.Send(tea.KeyMsg{Type: tea.KeyCtrlJ})

	ch := make(chan string, 1)
	p.Send(viewProbe{fn: func(m *model) { ch <- m.input.View() }})
	v := <-ch
	if !strings.Contains(v, "hello first line") {
		t.Fatalf("after ctrl+j the first line vanished from the input view: %q", strings.Split(v, "\n"))
	}
}

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }
