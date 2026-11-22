package tui

import (
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
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

// The "press again" hints are footer notices, not transcript lines: they go
// when the window closes or on the next key.
func TestPressAgainHintsAreTransient(t *testing.T) {
	m := compactCmdModel()
	m.queueSel = -1
	m.Update(mkWinSize(80, 24))
	blocks := len(m.blocks)

	m.key(tea.KeyMsg{Type: tea.KeyCtrlC})
	if len(m.blocks) != blocks {
		t.Fatal("the quit hint must not become a transcript line")
	}
	if !strings.Contains(ansi.Strip(m.View()), "Press Ctrl+C again to quit") {
		t.Fatalf("the quit hint should show in the footer:\n%s", ansi.Strip(m.View()))
	}
	m.Update(quitArmMsg{})
	if m.transientNotice != "" {
		t.Fatalf("the hint should go with the window: %q", m.transientNotice)
	}

	m.input.SetValue("draft")
	m.key(tea.KeyMsg{Type: tea.KeyEsc})
	if len(m.blocks) != blocks || !strings.Contains(m.transientNotice, "clear the input") {
		t.Fatalf("esc hint: blocks %d→%d, notice %q", blocks, len(m.blocks), m.transientNotice)
	}
	m.key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if m.transientNotice != "" {
		t.Fatalf("the next key should clear the hint: %q", m.transientNotice)
	}

	m.input.SetValue("")
	m.key(tea.KeyMsg{Type: tea.KeyEsc})
	if len(m.blocks) != blocks || !strings.Contains(m.transientNotice, "rewind") {
		t.Fatalf("rewind hint: blocks %d→%d, notice %q", blocks, len(m.blocks), m.transientNotice)
	}
	m.Update(escArmMsg{})
	if m.transientNotice != "" {
		t.Fatalf("the rewind hint should go with the window: %q", m.transientNotice)
	}
}
