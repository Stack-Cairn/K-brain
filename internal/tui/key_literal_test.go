package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// batchedKeys is text as bubbletea reads it when it arrives in one write
// (an input method commit, dictation, a laggy remote session): runs of runes
// split at spaces, each space its own key.
func batchedKeys(s string) []tea.KeyMsg {
	var out []tea.KeyMsg
	for i, word := range strings.Split(s, " ") {
		if i > 0 {
			out = append(out, tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
		}
		if word != "" {
			out = append(out, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(word)})
		}
	}
	return out
}

func typeBatched(m *model, s string) *model {
	for _, k := range batchedKeys(s) {
		tm, _ := m.Update(k)
		m = tm.(*model)
	}
	return m
}

func TestBatchedWordsThatNameKeysStayText(t *testing.T) {
	for _, text := range []string{
		"a up b",
		"x home y",
		"我们 up 吧",
		"follow up question",
		"press delete then end and left right down",
		"ctrl+a f2 alt+t alt+o shift+tab esc enter tab pgup backspace",
	} {
		m := modelCmdModel()
		m = typeBatched(m, text)
		if got := m.input.Value(); got != text {
			t.Errorf("batched %q became %q", text, got)
		}
		if m.overlayOwnsKeys() {
			t.Errorf("batched %q opened an overlay", text)
		}
	}
}

func TestBatchedKeyNameWhileBusyIsQueuedIntact(t *testing.T) {
	m := modelCmdModel()
	m.busy = true
	m = typeBatched(m, "follow up question")
	tm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = tm.(*model)
	if len(m.queue) != 1 || m.queue[0] != "follow up question" {
		t.Fatalf("queued message should be the typed text, got %+v", m.queue)
	}
}

func TestRealKeysStillActAfterLiteralRuns(t *testing.T) {
	m := modelCmdModel()
	m = typeBatched(m, "ab up")
	// A genuine Home key still moves the cursor; a single typed rune is text.
	tm, _ := m.Update(tea.KeyMsg{Type: tea.KeyHome})
	m = tm.(*model)
	tm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'X'}})
	m = tm.(*model)
	if got := m.input.Value(); got != "Xab up" {
		t.Fatalf("Home then X should insert at the start, got %q", got)
	}
	if k := literalRun(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("u")}); k.Paste {
		t.Fatal("a single rune must stay a key press")
	}
	if k := literalRun(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("jjk")}); k.Paste {
		t.Fatal("a burst that spells no key name must stay a burst of keys")
	}
}

func TestBatchedKeyNameIsTextInHistorySearch(t *testing.T) {
	m := modelCmdModel()
	m.hist = []string{"scroll up the log"}
	tm, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	m = tm.(*model)
	if m.cmp == nil || m.cmp.hs == nil {
		t.Fatal("Ctrl+R should open the history search")
	}
	m = typeBatched(m, "scroll up")
	if got := m.input.Value(); got != "scroll up" {
		t.Fatalf("history search query should hold the typed words, got %q", got)
	}
}
