package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func transcriptText(m *model) string {
	var b strings.Builder
	for _, bl := range m.blocks {
		b.WriteString(ansi.Strip(bl.text))
		b.WriteString("\n")
	}
	return b.String()
}

// A message that starts with a file path is sent to the model, not
// rejected as an unknown command.
func TestPathLikeLineIsSentAsMessage(t *testing.T) {
	for _, text := range []string{
		"/src/main.go crashes on start, why?",
		"/etc/hosts has a stale entry",
		"/README.md is out of date",
	} {
		m, _ := stClock(t)
		m = typeStr(t, m, text)
		tm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = tm.(*model)
		got := transcriptText(m)
		if strings.Contains(got, "unknown command") {
			t.Fatalf("%q was treated as a command:\n%s", text, got)
		}
		if !m.busy || !strings.Contains(got, text) {
			t.Fatalf("%q was not sent (busy=%v):\n%s", text, m.busy, got)
		}
	}
}

// An unknown /word is still an error, but the draft stays for fixing.
func TestUnknownCommandKeepsTheDraft(t *testing.T) {
	m, _ := stClock(t)
	m = typeStr(t, m, "/frobnicate the widgets")
	tm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = tm.(*model)
	if !strings.Contains(transcriptText(m), "unknown command /frobnicate") {
		t.Fatalf("no error shown:\n%s", transcriptText(m))
	}
	if m.busy {
		t.Fatal("an unknown command must not be sent")
	}
	if got := m.input.Value(); got != "/frobnicate the widgets" {
		t.Fatalf("draft lost: %q", got)
	}
}

// Esc on the slash menu opened by a lone "/" closes it and clears the "/".
func TestEscOnSlashMenuClearsLoneSlash(t *testing.T) {
	m := modelCmdModel()
	m = typeStr(t, m, "/")
	if m.menu == nil {
		t.Fatal("/ should open the command menu")
	}
	tm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = tm.(*model)
	if m.menu != nil || m.input.Value() != "" {
		t.Fatalf("menu %v, draft %q", m.menu != nil, m.input.Value())
	}
	// With more typed, Esc only closes the menu.
	m = typeStr(t, m, "/mo")
	tm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = tm.(*model)
	if m.menu != nil || m.input.Value() != "/mo" {
		t.Fatalf("menu %v, draft %q", m.menu != nil, m.input.Value())
	}
}
