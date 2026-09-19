package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestToolCallQueuedRowReplacedOnStart(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(80, 24))

	m.Update(toolCallMsg{id: "c1", name: "bash", args: `{"command":"make"}`})
	if len(m.blocks) == 0 || m.blocks[len(m.blocks)-1].kind != blockToolQueued {
		t.Fatal("toolCallMsg should append a queued row")
	}
	row := m.blocks[len(m.blocks)-1]
	if !strings.Contains(ansi.Strip(row.render(m.width)), "bash") {
		t.Fatalf("queued row should name the tool, got %q", ansi.Strip(row.render(m.width)))
	}

	before := len(m.blocks)
	m.Update(toolStartMsg{id: "c1", name: "bash", args: `{"command":"make"}`})
	if len(m.blocks) != before {
		t.Fatalf("toolStart should replace the queued row, not append (before=%d after=%d)", before, len(m.blocks))
	}
	last := m.blocks[len(m.blocks)-1]
	if last.kind != blockToolRun || !last.toolRunning {
		t.Fatalf("after toolStart the row should be a running row, got kind=%v running=%v", last.kind, last.toolRunning)
	}
}

func TestToolCallCommitsPendingContentBeforePreview(t *testing.T) {
	m := compactCmdModel()
	m.showThinking = true
	m.Update(textMsg("before tool"))
	m.Update(thinkMsg("plan tool"))
	m.Update(toolCallMsg{id: "c", name: "read", args: `{"path":"file"}`})
	if len(m.blocks) != 3 || m.blocks[0].text != "before tool" || !strings.Contains(m.blocks[1].text, "plan tool") || m.blocks[2].kind != blockToolQueued {
		t.Fatalf("preview overtook content: %+v", m.blocks)
	}
	if m.current != "" || m.curThink != "" {
		t.Fatal("preview left earlier content below the tool")
	}
}

func TestToolCallQueuedRowUpdatesInPlace(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(80, 24))

	deltas := []string{
		`{"command": "`,
		`{"command": "mkdir`,
		`{"command": "mkdir -p`,
		`{"command": "mkdir -p ~/.config/k9s"}`,
	}
	for _, args := range deltas {
		m.Update(toolCallMsg{id: "c1", name: "bash", args: args})
	}

	var queued int
	for _, b := range m.blocks {
		if b.kind == blockToolQueued {
			queued++
		}
	}
	if queued != 1 {
		t.Fatalf("expected 1 queued row after %d deltas, got %d", len(deltas), queued)
	}
	row := m.blocks[len(m.blocks)-1]
	got := ansi.Strip(row.render(m.width))
	if !strings.Contains(got, deltas[len(deltas)-1]) {
		t.Fatalf("queued row should show the latest args snapshot, got %q", got)
	}

	m.Update(toolCallMsg{id: "c2", name: "read", args: `{"path":"/tmp/x"}`})
	if n := len(m.blocks); n != 2 {
		t.Fatalf("a different tool-call id should append its own row, blocks=%d", n)
	}
}
