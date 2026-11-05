package tui

import (
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/charmbracelet/x/ansi"
)

func TestToolHeaderRowSubjects(t *testing.T) {
	cases := []struct{ name, args, want string }{
		{"edit", `{"path":"internal/tui/tui.go"}`, "Edit  internal/tui/tui.go"},
		{"write", `{"path":"a.go","content":"x"}`, "Write  a.go"},
		{"read", `{"path":"a.go"}`, "Read  a.go"},
		{"bash", `{"command":"git  status"}`, "Bash  git status"},
		{"subagent", `{"description":"probe the repo","prompt":"p"}`, "Subagent  probe the repo"},
		{"subagent_steer", `{"id":"sub-3","message":"m"}`, "Steer  sub-3"},
	}
	for _, c := range cases {
		if got := ansi.Strip(toolHeaderRow(c.name, c.args, false)); got != glyphs.ToolDone+" "+c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, glyphs.ToolDone+" "+c.want)
		}
	}
	// A failure changes the glyph only; the row keeps its grammar.
	if got := ansi.Strip(toolHeaderRow("bash", `{"command":"false"}`, true)); got != glyphs.Fail+" Bash  false" {
		t.Errorf("failed header: %q", got)
	}
}

func TestExtractDiffAndCounts(t *testing.T) {
	result := "Replaced 1 occurrence(s) in a.go\n```diff\n11   ctx\n12 - old\n12 + new\n13 + more\n14   ctx\n```\na.go:12: some diagnostic"
	diff, rest := extractDiff(result)
	if diff == "" || !strings.Contains(rest, "diagnostic") || strings.Contains(rest, "```") {
		t.Fatalf("extract: diff=%q rest=%q", diff, rest)
	}
	add, del := diffCounts(diff)
	if add != 2 || del != 1 {
		t.Fatalf("counts: +%d -%d", add, del)
	}

	if diffLineKind("- x") != '-' || diffLineKind("+ x") != '+' || diffLineKind("… +3 more lines") != ' ' {
		t.Fatal("diffLineKind misclassifies")
	}
}

func TestEditResultRendersDiff(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(80, 24))
	m.Update(toolStartMsg{id: "e1", name: "edit", args: `{"path":"a.go","old_string":"x","new_string":"y"}`})
	m.Update(toolEndMsg{
		id: "e1", name: "edit",
		result: "Replaced 1 occurrence(s) in a.go\n```diff\n3   ctx\n4 - x\n4 + y\n```\na.go:4: unused variable",
	})

	run := m.blocks[len(m.blocks)-2]
	if got := ansi.Strip(run.render(m.width)); !strings.HasPrefix(got, glyphs.ToolDone+" Edit  a.go") {
		t.Fatalf("run row: %q", got)
	}
	res := ansi.Strip(m.blocks[len(m.blocks)-1].render(m.width))
	// The counts ride on the header; the result is the diff itself.
	if got := ansi.Strip(m.blocks[len(m.blocks)-2].render(m.width)); !strings.Contains(got, "+1 "+glyphs.Minus+"1") {
		t.Fatalf("run row should carry the counts: %q", got)
	}
	for _, want := range []string{"4  - x", "4  + y", "unused variable"} {
		if !strings.Contains(res, want) {
			t.Fatalf("result block missing %q:\n%s", want, res)
		}
	}
	if strings.Contains(res, "Added 1 line") {
		t.Fatalf("a result under its header should not repeat the counts:\n%s", res)
	}
}

func TestDiffPreviewCapAndExpand(t *testing.T) {
	var rows []string
	for range diffPreviewRows + 5 {
		rows = append(rows, "+ added")
	}
	b := block{kind: blockTool, text: "Wrote 1 bytes to a.go\n```diff\n" + strings.Join(rows, "\n") + "\n```"}
	out := ansi.Strip(b.render(80))
	// Drawn without a header, the result says the counts itself.
	if !strings.Contains(out, glyphs.Fold+" +5 lines · ctrl+e") || !strings.Contains(out, "Added 35 lines") {
		t.Fatalf("collapsed diff should cap:\n%s", out)
	}
	b.expanded, b.stale = true, true
	if out := ansi.Strip(b.render(80)); strings.Contains(out, "lines · ctrl+e") {
		t.Fatal("expanded diff should show everything")
	}
}

func TestSeedTranscriptShowsDiffs(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(80, 24))
	call := ai.Message{Role: "assistant"}
	var tc ai.ToolCall
	tc.Function.Name = "edit"
	tc.Function.Arguments = `{"path":"a.go"}`
	call.ToolCalls = []ai.ToolCall{tc}
	res := ai.Message{Role: "tool", Name: "edit", Content: "Replaced 1 occurrence(s) in a.go\n```diff\n4 - x\n4 + y\n```"}
	m.seedTranscript([]ai.Message{call, res}, 1)

	var joined strings.Builder
	for i := range m.blocks {
		joined.WriteString(ansi.Strip(m.blocks[i].render(m.width)) + "\n")
	}
	for _, want := range []string{glyphs.ToolDone + " Edit  a.go", "+1 " + glyphs.Minus + "1", "4  + y"} {
		if !strings.Contains(joined.String(), want) {
			t.Fatalf("resumed transcript missing %q:\n%s", want, joined.String())
		}
	}
}

func TestToolResultFailed(t *testing.T) {
	for result, want := range map[string]bool{
		"Error: exit status 1":                     true,
		"vet: unused x\n\n((exit: exit status 1))": true,
		"out\n(exit: exit status 2)\n":             true,
		"ok  pkg 2.1s":                             false,
		"mentions (exit: soon) mid-line\nmore":     false,
	} {
		if got := toolResultFailed(result); got != want {
			t.Errorf("toolResultFailed(%q) = %v, want %v", result, got, want)
		}
	}
}
