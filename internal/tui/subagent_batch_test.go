package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestSubagentBatchNumbered(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(100, 24))

	descs := map[string]string{
		"subagent_1": "survey context in pi",
		"subagent_2": "survey context in codex",
		"subagent_3": "survey context in exo",
	}

	for _, id := range []string{"subagent_1", "subagent_2", "subagent_3"} {
		m.Update(toolCallMsg{id: id, name: "subagent", args: `{"description":"` + descs[id] + `","prompt":"p"}`})
	}
	var rows []string
	for _, b := range m.blocks {
		if b.kind == blockToolQueued {
			rows = append(rows, ansi.Strip(b.render(m.width)))
		}
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 queued rows, got %d", len(rows))
	}
	for i, id := range []string{"subagent_1", "subagent_2", "subagent_3"} {
		want := []string{"1/3", "2/3", "3/3"}[i]
		if !strings.Contains(rows[i], want) {
			t.Errorf("queued row %d should carry %q, got %q", i, want, rows[i])
		}
		if !strings.Contains(rows[i], descs[id]) {
			t.Errorf("queued row %d should show the task description %q, got %q", i, descs[id], rows[i])
		}
		if strings.Contains(rows[i], "{") {
			t.Errorf("queued row %d should not show raw JSON, got %q", i, rows[i])
		}
	}

	m.Update(toolStartMsg{id: "subagent_1", name: "subagent", args: `{"description":"` + descs["subagent_1"] + `","prompt":"p"}`})
	last := m.blocks[len(m.blocks)-1]
	if last.kind != blockToolRun {
		t.Fatal("toolStart should append a running row")
	}
	got := ansi.Strip(last.render(m.width))
	if !strings.Contains(got, "1/3") {
		t.Fatalf("running row should keep its batch number, got %q", got)
	}
	if !strings.Contains(got, descs["subagent_1"]) {
		t.Fatalf("running row should show the task description, got %q", got)
	}
}

func TestSubagentSingletonNotNumbered(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(100, 24))
	m.Update(toolCallMsg{id: "subagent_1", name: "subagent", args: `{"description":"d","prompt":"p"}`})
	got := ansi.Strip(m.blocks[len(m.blocks)-1].render(m.width))
	if strings.Contains(got, "/") {
		t.Fatalf("singleton subagent should not be numbered, got %q", got)
	}
}

func TestBatchSuffixPerToolName(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(100, 24))
	m.Update(toolCallMsg{id: "a", name: "read", args: `{"path":"x"}`})
	m.Update(toolCallMsg{id: "b", name: "read", args: `{"path":"y"}`})
	m.Update(toolCallMsg{id: "c", name: "bash", args: `{"command":"true"}`})
	rows := map[string]string{}
	for _, b := range m.blocks {
		if b.kind == blockToolQueued {
			rows[b.toolID] = ansi.Strip(b.render(m.width))
		}
	}
	if !strings.Contains(rows["a"], "1/2") || !strings.Contains(rows["b"], "2/2") {
		t.Errorf("reads should number 1/2 and 2/2, got a=%q b=%q", rows["a"], rows["b"])
	}
	if strings.Contains(rows["c"], "/") {
		t.Errorf("the lone bash call should not be numbered, got %q", rows["c"])
	}
}
