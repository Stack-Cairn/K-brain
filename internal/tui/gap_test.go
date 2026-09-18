package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestNoGapBetweenLastReplyAndInput(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(80, 24))
	m.append(" ❯ hi")
	m.appendAssistantBlock("Hi! What can I help you with today?")
	m.append(" ❯ how are you")
	m.appendAssistantBlock("Doing well, thanks for asking! Ready to dig into some code whenever you are. What are you working on?")
	m.layout()

	lines := strings.Split(ansi.Strip(m.View()), "\n")

	inputRow, lastReplyRow := -1, -1
	for i, l := range lines {
		if strings.Contains(l, "Ask k-brain anything") {
			inputRow = i
		}
		if strings.Contains(l, "What are you working on?") {
			lastReplyRow = i
		}
	}
	if inputRow < 0 || lastReplyRow < 0 {
		t.Fatalf("could not find reply (%d) or input (%d) rows:\n%s", lastReplyRow, inputRow, strings.Join(lines, "\n"))
	}

	if gap := inputRow - lastReplyRow - 1; gap > 1 {
		t.Fatalf("found %d blank rows between last reply (row %d) and input (row %d):\n%s",
			gap, lastReplyRow, inputRow, strings.Join(lines, "\n"))
	}
}

func TestViewportViewHasNoTrailingBlankRows(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(80, 24))
	m.append(" ❯ hi")
	m.appendAssistantBlock("Short reply.")
	m.layout()

	rendered := m.viewportView()
	lines := strings.Split(rendered, "\n")
	if last := lines[len(lines)-1]; strings.TrimSpace(ansi.Strip(last)) == "" {
		t.Fatalf("viewport render still has a trailing blank row: %q", rendered)
	}
}
