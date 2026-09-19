package tui

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func TestResumeShowsInterruptedToolCalls(t *testing.T) {
	m := compactCmdModel()
	st, err := session.Open(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	m.store = st

	call := func(id, name string) ai.ToolCall {
		var tc ai.ToolCall
		tc.ID, tc.Function.Name = id, name
		tc.Function.Arguments = `{"path":"x.go"}`
		return tc
	}
	id, _ := st.Create("/tmp", m.modelName, m.provName)
	msgs := []ai.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "fix the tests", Authored: true},
		{Role: "assistant", ToolCalls: []ai.ToolCall{call("c1", "read"), call("c2", "bash")}},
		{Role: "tool", Content: "file body", ToolCallID: "c1", Name: "read"},
	}
	if err := st.Save(id, 1, msgs, m.modelName, m.provName); err != nil {
		t.Fatal(err)
	}

	if err := m.resume(id); err != nil {
		t.Fatal(err)
	}

	var rows []string
	for _, b := range m.blocks {
		rows = append(rows, ansi.Strip(b.render(m.width)))
	}
	var sawCallRow, sawNote bool
	for _, r := range rows {
		if strings.Contains(r, "⚒ bash") && strings.Contains(r, "interrupted") {
			sawCallRow = true
		}
		if strings.Contains(r, "1 tool call(s) were interrupted") && strings.Contains(r, "can retry") {
			sawNote = true
		}
	}
	if !sawCallRow {
		t.Fatalf("transcript missing the inline interrupted row for bash:\n%s", strings.Join(rows, "\n"))
	}
	if !sawNote {
		t.Fatalf("transcript missing the resume summary note:\n%s", strings.Join(rows, "\n"))
	}

	for _, r := range rows {
		if strings.Contains(r, "⚒ read") && strings.Contains(r, "interrupted") {
			t.Fatalf("answered tool mislabeled as interrupted: %q", r)
		}
	}
}
