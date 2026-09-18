package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func TestCtrlJFirstLineVisibleWithTranscript(t *testing.T) {
	m := compactCmdModel()
	m.queueSel = -1
	m.agent.Messages = append(m.agent.Messages,
		ai.Message{Role: "user", Content: "earlier question", Authored: true},
		ai.Message{Role: "assistant", Content: "earlier answer with several lines\nof content\nright here"},
	)
	m.height = 24
	m.layout()
	t.Logf("layout: vp.Height=%d inputHeight=%d", m.vp.Height, m.input.Height())
	_ = lipgloss.Height

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
		t.Fatalf("REPRODUCED with transcript: %q", strings.Split(v, "\n"))
	}
	t.Logf("view ok: %q", strings.Split(v, "\n"))
}
