package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// Every Alt shortcut that matters has a twin that needs no Option-as-Meta:
// Ctrl+] for Alt+T, Ctrl+F for Alt+O, Shift+↑ for Alt+↑, Ctrl+E for Alt+E.
// The twins take a key only where it did nothing before, so the composer's
// own Ctrl+F, Shift+↑ and Ctrl+E still work on a draft.

func TestCtrlBracketTogglesTopPanel(t *testing.T) {
	m, _ := topPanelModel(t, 100, 30)
	before := m.panelExpanded()
	m = press(t, m, tea.KeyMsg{Type: tea.KeyCtrlCloseBracket})
	if m.panelExpanded() == before {
		t.Fatal("Ctrl+] did not toggle the top panel")
	}
	m = press(t, m, tea.KeyMsg{Type: tea.KeyCtrlCloseBracket})
	if m.panelExpanded() != before {
		t.Fatal("a second Ctrl+] did not toggle it back")
	}
}

func TestCtrlFExpandsLastToolOnEmptyComposer(t *testing.T) {
	m := compactCmdModel()
	m.blocks = append(m.blocks, block{kind: blockTool, text: "ran a thing"})
	m = press(t, m, tea.KeyMsg{Type: tea.KeyCtrlF})
	if !m.blocks[len(m.blocks)-1].expanded {
		t.Fatal("Ctrl+F on an empty composer should expand the last tool result")
	}
	m = press(t, m, tea.KeyMsg{Type: tea.KeyCtrlF})
	if m.blocks[len(m.blocks)-1].expanded {
		t.Fatal("a second Ctrl+F should fold it")
	}

	// With a draft, Ctrl+F is still the cursor's forward.
	m.input.SetValue("hello")
	m.input.CursorStart()
	m = press(t, m, tea.KeyMsg{Type: tea.KeyCtrlF})
	if m.blocks[len(m.blocks)-1].expanded {
		t.Fatal("Ctrl+F on a draft must not expand the tool result")
	}
	if got := m.input.LineInfo().CharOffset; got != 1 {
		t.Fatalf("Ctrl+F on a draft should move the cursor one right, offset %d", got)
	}
}

func TestShiftUpFocusesTranscriptOnEmptyComposer(t *testing.T) {
	m := focusModel(80, 24)
	m = press(t, m, tea.KeyMsg{Type: tea.KeyShiftUp})
	if m.txs().focus == nil {
		t.Fatal("Shift+↑ on an empty composer should focus the transcript")
	}

	m = focusModel(80, 24)
	m.input.SetValue("line one\nline two")
	m = press(t, m, tea.KeyMsg{Type: tea.KeyShiftUp})
	if m.txs().focus != nil {
		t.Fatal("Shift+↑ on a draft selects in the composer; it must not take focus")
	}

	// The queue pane keeps Shift+↑ for moving the selected message.
	q := busyQueueModel("first", "second")
	q.queueSel = 1
	q = press(t, q, tea.KeyMsg{Type: tea.KeyShiftUp})
	if q.txs().focus != nil || strings.Join(q.queue, ",") != "second,first" {
		t.Fatalf("Shift+↑ in the queue pane should move the message: %v focus=%v", q.queue, q.txs().focus != nil)
	}
}

func TestCtrlEEditsLastQueuedOnEmptyComposer(t *testing.T) {
	m := busyQueueModel("first", "second")
	m = press(t, m, tea.KeyMsg{Type: tea.KeyCtrlE})
	if m.input.Value() != "second" || m.editingQueued() == nil {
		t.Fatalf("Ctrl+E (busy, empty composer) should edit the last queued message, composer %q", m.input.Value())
	}

	// With a draft it is the end of the line, as before.
	m = busyQueueModel("first", "second")
	m.input.SetValue("draft")
	m.input.CursorStart()
	m = press(t, m, tea.KeyMsg{Type: tea.KeyCtrlE})
	if m.input.Value() != "draft" || m.editingQueued() != nil || m.input.LineInfo().CharOffset != len("draft") {
		t.Fatalf("Ctrl+E on a draft should go to the line end: %q at %d", m.input.Value(), m.input.LineInfo().CharOffset)
	}
}

// The approval card and a folded tool result name the same expand key.
func TestExpandHintIsTheSameEverywhere(t *testing.T) {
	if !strings.Contains(foldHint(5, "lines", true), expandKeyHint) {
		t.Fatal("the fold row does not name the expand key")
	}
	m := compactCmdModel()
	for _, h := range []string{m.tr(" · ctrl+f expand"), m.tr(" · ctrl+f fold")} {
		if !strings.Contains(h, expandKeyHint) {
			t.Fatalf("card hint %q does not name %q", h, expandKeyHint)
		}
	}
}
