package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func pastedLog(n int) string {
	var lines []string
	for i := 1; i <= n; i++ {
		lines = append(lines, fmt.Sprintf("log line %04d: request served", i))
	}
	return strings.Join(lines, "\n")
}

// A 2000-line paste sent from the composer shows in the transcript as it
// was typed, chip and all, and opens to the whole text; the turn carries the
// whole text.
func TestPastedPromptStaysFolded(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	m := compactCmdModel()
	m.Update(mkWinSize(100, 30))
	for _, r := range "summarize this log " {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	log := pastedLog(2000)
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(log), Paste: true})
	if !strings.Contains(m.input.Value(), "[Pasted 2000 lines") {
		t.Fatalf("setup: no chip in %q", m.input.Value())
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	var b *block
	for i := range m.blocks {
		if m.blocks[i].kind == blockUser {
			b = &m.blocks[i]
		}
	}
	if b == nil {
		t.Fatal("no prompt in the transcript")
	}
	if !strings.Contains(b.text, "log line 2000: request served") {
		t.Fatal("the prompt block lost the pasted text")
	}
	folded := ansi.Strip(b.render(100))
	if !strings.Contains(folded, "summarize this log [Pasted 2000 lines]") || strings.Contains(folded, "log line 0001") {
		t.Fatalf("folded prompt:\n%s", folded)
	}
	if n := strings.Count(folded, "\n") + 1; n > 2 {
		t.Fatalf("folded prompt is %d rows", n)
	}
	if !b.toggle() {
		t.Fatal("the pasted prompt does not open")
	}
	open := ansi.Strip(b.render(100))
	if !strings.Contains(open, "log line 2000: request served") {
		t.Fatal("opened, the prompt does not show the whole paste")
	}

	// The model is sent the whole text.
	var sent string
	for _, msg := range m.agent.MessagesSnapshot() {
		if msg.Role == "user" {
			sent = msg.TextContent()
		}
	}
	if !strings.Contains(sent, "log line 2000: request served") {
		t.Fatalf("the turn was sent the chip, not the paste: %.80q", sent)
	}
}

// A long prompt restored from history folds to its first lines; a short one
// draws in full and keeps the generic fold.
func TestLongPromptFoldsOnReplay(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(100, 30))
	m.blocks = nil
	m.seedTranscript([]ai.Message{
		{Role: "user", Content: "short one", Authored: true},
		{Role: "user", Content: pastedLog(300), Authored: true},
	}, 0)
	var users []*block
	for i := range m.blocks {
		if m.blocks[i].kind == blockUser {
			users = append(users, &m.blocks[i])
		}
	}
	if len(users) != 2 {
		t.Fatalf("%d prompts", len(users))
	}
	if hasToggle(users[0]) {
		t.Fatal("a short prompt has nothing to fold")
	}
	long := ansi.Strip(users[1].render(100))
	if !strings.Contains(long, "log line 0008") || strings.Contains(long, "log line 0009") || !strings.Contains(long, "+292 lines") {
		t.Fatalf("long prompt folded to:\n%s", long)
	}
	users[1].toggle()
	if !strings.Contains(ansi.Strip(users[1].render(100)), "log line 0300") {
		t.Fatal("opened, the long prompt does not show its end")
	}
}
