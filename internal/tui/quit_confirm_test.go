package tui

import (
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	tea "github.com/charmbracelet/bubbletea"
)

func idleModel() *model {
	m := &model{
		input: newInput(),
		agent: &agent.Agent{},
	}
	m.width = 80
	m.input.SetWidth(m.width - 2)
	return m
}

func TestCtrlCRequiresDoublePressToQuit(t *testing.T) {
	m := idleModel()

	tm, cmd := m.key(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = tm.(*model)
	if !m.quit1 {
		t.Fatal("first ctrl+c should arm quit1")
	}
	if cmd != nil {

		if _, isQuit := cmd().(tea.QuitMsg); isQuit {
			t.Fatal("first ctrl+c must not quit")
		}
	}

	tm, cmd = m.key(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = tm.(*model)
	if cmd == nil {
		t.Fatal("second ctrl+c should produce a quit command")
	}
	if _, isQuit := cmd().(tea.QuitMsg); !isQuit {
		t.Fatalf("second ctrl+c should quit, got %T", cmd())
	}
	if m.quit1 {
		t.Fatal("quit1 should clear after the confirming press")
	}
}

func TestCtrlCArmExpires(t *testing.T) {
	m := idleModel()
	tm, _ := m.key(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = tm.(*model)
	if !m.quit1 {
		t.Fatal("first press should arm")
	}

	tm2, _ := m.Update(quitArmMsg{})
	m = tm2.(*model)
	if m.quit1 {
		t.Fatal("quitArmMsg should disarm quit1")
	}

	tm, cmd := m.key(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = tm.(*model)
	if !m.quit1 {
		t.Fatal("post-expiry press should re-arm, not quit")
	}
	if cmd != nil {
		if _, isQuit := cmd().(tea.QuitMsg); isQuit {
			t.Fatal("post-expiry single press must not quit")
		}
	}
}

func TestCtrlCBusyInterruptsNotQuits(t *testing.T) {
	m := idleModel()
	m.busy = true
	cancelled := false
	m.cancel = func() { cancelled = true }

	_, cmd := m.key(tea.KeyMsg{Type: tea.KeyCtrlC})
	if !m.interrupt1 {
		t.Fatal("busy first press should arm interrupt1")
	}
	if cancelled {
		t.Fatal("busy first press must not cancel yet")
	}
	if cmd != nil {
		if _, isQuit := cmd().(tea.QuitMsg); isQuit {
			t.Fatal("busy path must never quit")
		}
	}

	_, cmd = m.key(tea.KeyMsg{Type: tea.KeyCtrlC})
	if !cancelled {
		t.Fatal("busy second press should cancel the turn")
	}
	if cmd != nil {
		if _, isQuit := cmd().(tea.QuitMsg); isQuit {
			t.Fatal("busy path must never quit")
		}
	}
}
