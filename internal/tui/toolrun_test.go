package tui

import (
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func TestToolRowCollapsesOnCompletion(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(80, 24))

	m.Update(toolStartMsg{id: "c1", name: "read", args: `{"path":"foo.go"}`})
	if len(m.blocks) == 0 || m.blocks[len(m.blocks)-1].kind != blockToolRun {
		t.Fatal("toolStart should append a running row")
	}
	row := m.blocks[len(m.blocks)-1]
	if !row.toolRunning {
		t.Fatal("row should be running")
	}
	if got := ansi.Strip(row.render(m.width)); !strings.HasPrefix(got, glyphs.ToolRun+" Read  foo.go") {
		t.Fatalf("running row should show the running glyph and the call, got %q", got)
	}

	m.Update(toolEndMsg{id: "c1", name: "read", result: "file body\nline2\nline3\nline4\nline5\nline6"})
	row = m.blocks[len(m.blocks)-2]
	if row.toolRunning {
		t.Fatal("completion should stop the run state")
	}
	got := ansi.Strip(row.render(m.width))
	if strings.Count(got, "\n") > 0 {
		t.Fatalf("completed row should collapse to one line, got %q", got)
	}

	if !strings.Contains(got, glyphs.ToolDone+" Read  foo.go") {
		t.Fatalf("completed row should keep the call header, got %q", got)
	}

	res := m.blocks[len(m.blocks)-1]
	if res.kind != blockTool {
		t.Fatal("the result block should follow the run row")
	}
	got = ansi.Strip(res.render(m.width))
	if !strings.Contains(got, glyphs.Result+" 6 lines") || strings.Contains(got, "file body") {
		t.Fatalf("a read result should fold to its line count under the elbow, got %q", got)
	}
	res.toggle()
	if got := ansi.Strip(res.render(m.width)); !strings.Contains(got, glyphs.Result+" file body") || !strings.Contains(got, "line6") {
		t.Fatalf("an expanded read result should show the file, got %q", got)
	}
	if !row.toggle() {
		t.Fatal("ctrl+e should still toggle the collapsed row")
	}
}

func TestToolRowFailureIsRed(t *testing.T) {
	// Rows are styled when their event lands, so the profile is set first.
	restore := useProfile(t, termenv.ANSI256, true)
	defer restore()
	m := compactCmdModel()
	m.Update(mkWinSize(80, 24))
	m.Update(toolStartMsg{id: "c1", name: "bash", args: `{"command":"false"}`})
	m.Update(toolEndMsg{id: "c1", name: "bash", result: "Error: exit status 1"})
	var run *block
	for i := range m.blocks {
		if m.blocks[i].kind == blockToolRun {
			run = &m.blocks[i]
		}
	}
	if run == nil || !run.toolFailed {
		t.Fatal("a failed tool should mark the collapsed row")
	}
	got := ansi.Strip(run.render(m.width))
	if !strings.HasPrefix(got, glyphs.Fail+" Bash  false") || !strings.Contains(got, "failed") {
		t.Fatalf("failed row should keep the call header and say it failed, got %q", got)
	}
	// Only the glyph and the meta carry the error colour, not the whole row.
	raw := run.render(m.width)
	if !strings.Contains(raw, errorTitleStyle.Render(glyphs.Fail)) || strings.Contains(raw, errStyle.Render("Bash")) || strings.Contains(raw, errStyle.Render("false")) {
		t.Fatalf("only the glyph and meta should carry the error colour: %q", raw)
	}

	res := m.blocks[len(m.blocks)-1]
	if got := ansi.Strip(res.render(m.width)); !strings.Contains(got, "Error: exit status 1") {
		t.Fatalf("result block should carry the error text, got %q", got)
	}
}

func TestToolBlocksGlueWithoutBlankRows(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(80, 30))
	m.appendAssistantBlock("before")
	m.Update(toolStartMsg{id: "1", name: "read", args: `{"path":"a.go"}`})
	m.Update(toolEndMsg{id: "1", name: "read", result: "x\ny\n"})
	m.Update(toolStartMsg{id: "2", name: "bash", args: `{"command":"ls"}`})
	m.Update(toolEndMsg{id: "2", name: "bash", result: "ok"})
	m.appendAssistantBlock("after")
	rows := m.viewportPlain()
	for i := range rows {
		rows[i] = strings.TrimRight(rows[i], " ")
	}
	got := stripToolMeta(strings.Join(rows, "\n"))
	d, r, a := glyphs.ToolDone, glyphs.Result, glyphs.Assistant
	want := d + " Read  a.go\n  " + r + " 2 lines\n" + d + " Bash  ls\n  " + r + " ok\n\n" + a + " after"
	if !strings.Contains(got, want) {
		t.Fatalf("tool group should render without blank rows:\n%s", got)
	}
	if !strings.Contains(got, a+" before\n\n"+d+" Read") {
		t.Fatalf("prose stays separated from the tool group:\n%s", got)
	}
}

func TestSeedTranscriptHidesReminderOnlyUserMessages(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(80, 30))
	m.seedTranscript([]ai.Message{
		{Role: "user", Content: "real question", Authored: true},
		{Role: "assistant", Content: "partial"},
		{Role: "user", Content: agent.WrapReminder("Continue exactly where it stopped.")},
		{Role: "assistant", Content: "rest"},
	}, 0)
	for _, b := range m.blocks {
		if strings.Contains(b.text, "Continue exactly") {
			t.Fatalf("a reminder-only message leaked into the transcript: %q", b.text)
		}
	}
	if !strings.Contains(strings.Join(m.viewportPlain(), "\n"), "real question") {
		t.Fatal("authored user messages must stay")
	}
}

func TestSearchResultsFoldToTheirOwnCounts(t *testing.T) {
	cases := []struct{ name, args, result, head, fold string }{
		{"grep", `{"pattern":"TODO","path":"internal"}`, "matches=3 files=2\nGrep: TODO\noffset=0 total=3 hasMore=false\na.go:1: TODO\na.go:9: TODO\nb.go:2: TODO", "Grep  TODO in internal", glyphs.Result + " 3 matches"},
		{"glob", `{"pattern":"**/*.go"}`, "Glob: **/*.go\noffset=0 total=1 hasMore=false\na.go", "Glob  **/*.go", glyphs.Result + " 1 file"},
		{"list", `{}`, "List: /w\noffset=0 total=2 hasMore=false\n[DIR] a\n[FILE] a/b.go", "List  .", glyphs.Result + " 2 entries"},
	}
	for _, c := range cases {
		m := compactCmdModel()
		m.Update(mkWinSize(80, 30))
		m.Update(toolStartMsg{id: "1", name: c.name, args: c.args})
		m.Update(toolEndMsg{id: "1", name: c.name, result: c.result})
		head := ansi.Strip(m.blocks[len(m.blocks)-2].render(m.width))
		fold := ansi.Strip(m.blocks[len(m.blocks)-1].render(m.width))
		if !strings.Contains(head, c.head) || !strings.Contains(fold, c.fold) {
			t.Errorf("%s: header %q fold %q, want %q / %q", c.name, head, fold, c.head, c.fold)
		}
	}
}

// stripToolMeta drops the right-aligned duration cell from finished tool rows
// so layout assertions do not depend on wall-clock time.
func stripToolMeta(s string) string {
	return toolMetaRE.ReplaceAllString(s, "")
}

var toolMetaRE = regexp.MustCompile(`(?m)[ ]+(` + regexp.QuoteMeta(unicodeGlyphs.OK) + `|failed)( · | )?[0-9.ms]*s?[ ]*$`)

func TestRunningRowKeepsItsBatchPlace(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(80, 24))
	m.Update(toolCallMsg{id: "c1", name: "bash", args: `{"command":"./scripts/test.sh"}`})
	m.Update(toolCallMsg{id: "c2", name: "bash", args: `{"command":"./scripts/vet.sh"}`})
	m.Update(toolStartMsg{id: "c1", name: "bash", args: `{"command":"./scripts/test.sh"}`})
	var order []string
	for _, b := range m.blocks {
		if b.toolID != "" {
			order = append(order, b.toolID)
		}
	}
	if strings.Join(order, ",") != "c1,c2" {
		t.Fatalf("the running call should stay above the one queued behind it, got %v", order)
	}
	if m.blocks[len(m.blocks)-1].kind != blockToolQueued {
		t.Fatal("the queued call stays last")
	}
}
