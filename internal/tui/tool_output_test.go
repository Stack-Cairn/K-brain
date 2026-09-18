package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestToolOutputMsgUpdatesRunningRow(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(80, 24))

	m.Update(toolStartMsg{id: "c1", name: "bash", args: `{"command":"make"}`})

	m.Update(toolOutputMsg{id: "zzz", text: "stray"})
	row := m.blocks[len(m.blocks)-1]
	if row.live != "" {
		t.Fatalf("unknown id must not set live output, got %q", row.live)
	}

	m.Update(toolOutputMsg{id: "c1", text: "compiling a.c\ncompiling b.c\nlinking"})
	row = m.blocks[len(m.blocks)-1]
	if row.live == "" {
		t.Fatal("toolOutputMsg should set the live tail on the running row")
	}
	got := ansi.Strip(row.render(m.width))
	if !strings.Contains(got, "linking") || !strings.Contains(got, "Running") && !strings.Contains(got, "⚒") {
		t.Fatalf("running row should show verb + live tail, got %q", got)
	}

	m.Update(toolOutputMsg{id: "c1", text: "line1\nline2\nline3\nline4\nline5"})
	row = m.blocks[len(m.blocks)-1]
	if strings.Contains(row.live, "line1") || !strings.Contains(row.live, "line5") {
		t.Fatalf("live tail should keep only the last lines: %q", row.live)
	}

	m.Update(toolEndMsg{id: "c1", name: "bash", result: "done\n"})
	row = m.blocks[len(m.blocks)-2]
	if row.live != "" {
		t.Fatalf("toolEndMsg should clear the live tail, got %q", row.live)
	}
	if got := ansi.Strip(row.render(m.width)); strings.Count(got, "\n") > 0 {
		t.Fatalf("completed row should collapse to one line, got %q", got)
	}
}

func TestLastLines(t *testing.T) {
	got := lastLines("a\n\nb\nc\n", 2)
	if got != "b\n  c" {
		t.Fatalf("lastLines: %q", got)
	}
	if lastLines("", 3) != "" {
		t.Fatal("empty input should give empty tail")
	}
	long := strings.Repeat("x", 300)
	if got := lastLines(long, 1); len(got) > 210 {
		t.Fatalf("lastLines should cap each line, got %d chars", len(got))
	}
}
