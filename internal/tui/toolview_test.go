package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// The tool view goldens pin each tool family's rows, every outcome included,
// at a narrow and a wide width. Each file holds a family's variants one after
// another, headed by their names. Regenerate with:
//
//	go test ./internal/tui -run TestToolViewGoldens -update-golden

// toolStage drives one scene: the model, and the clock the tool timer reads.
type toolStage struct {
	m     *model
	clock *time.Time
}

func (s toolStage) tick(d time.Duration) { *s.clock = s.clock.Add(d) }

// call runs one tool call through the stream messages a turn sends: start,
// the text result, then the structured result when r is given.
func (s toolStage) call(id, name, args string, d time.Duration, result string, r *tools.Result) {
	s.m.Update(toolStartMsg{id: id, name: name, args: args})
	s.tick(d)
	s.m.Update(toolEndMsg{id: id, name: name, result: result})
	if r != nil {
		r.Text = result
		if r.Duration == 0 {
			r.Duration = d
		}
		s.m.Update(toolResultMsg{id: id, name: name, result: *r})
	}
}

func exitCode(n int) *int { return &n }

type toolVariant struct {
	name  string
	build func(s toolStage)
}

func numberedLines(prefix string, n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "%s %d\n", prefix, i)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

var toolViewFamilies = []struct {
	name     string
	variants []toolVariant
}{
	{"shell", []toolVariant{
		{"ok", func(s toolStage) {
			s.call("b1", "bash", `{"command":"go test ./internal/tui/"}`, 2400*time.Millisecond,
				"ok  \tgithub.com/acme/kn/internal/tui\t2.1s", &tools.Result{ExitCode: exitCode(0)})
		}},
		{"exit", func(s toolStage) {
			s.call("b2", "bash", `{"command":"make lint"}`, 2800*time.Millisecond,
				numberedLines("lint: warning", 11)+"\nmake: *** [lint] Error 2\n(exit: exit status 2)", &tools.Result{Failed: true, ExitCode: exitCode(2)})
		}},
		{"timed-out", func(s toolStage) {
			s.call("b3", "bash", `{"command":"sleep 100 && echo done","timeout":30}`, 30*time.Second,
				"waiting for the lock\n(command timed out)", &tools.Result{Failed: true, TimedOut: true})
		}},
		{"cancelled", func(s toolStage) {
			s.call("b4", "Bash", `{"command":"npm install"}`, 400*time.Millisecond,
				"added 12 packages\n(exit: cancelled)", &tools.Result{Failed: true, Cancelled: true})
		}},
		{"interrupted", func(s toolStage) {
			s.m.Update(toolStartMsg{id: "b5", name: "bash", args: `{"command":"go build ./..."}`})
			s.tick(1500 * time.Millisecond)
			s.m.settleRunningTools()
		}},
		{"running", func(s toolStage) {
			s.m.Update(toolStartMsg{id: "b6", name: "bash", args: `{"command":"go test -race ./..."}`})
			s.m.Update(toolOutputMsg{id: "b6", text: "=== RUN   TestOne\n=== RUN   TestTwo"})
			s.tick(4 * time.Second)
		}},
		{"no-output", func(s toolStage) {
			s.call("b7", "bash", `{"command":"true"}`, 30*time.Millisecond, "(no output)", &tools.Result{ExitCode: exitCode(0)})
		}},
		{"process", func(s toolStage) {
			s.call("p1", "ManagedProcess", `{"action":"start","command":"npm run dev","name":"web"}`, 200*time.Millisecond,
				"started web (pid 4242)", &tools.Result{})
		}},
	}},
	{"edit", []toolVariant{
		{"edit", func(s toolStage) {
			s.call("e1", "edit", `{"path":"cmd/kn/status.go","old_string":"x","new_string":"y"}`, 80*time.Millisecond,
				"Replaced 1 occurrence(s) in cmd/kn/status.go\n```diff\n41   \tflags := pflag.NewFlagSet(\"status\")\n42 - \tverbose := flags.Bool(\"v\", false, \"verbose output for every subcommand\")\n42 + \tasJSON := flags.Bool(\"json\", false, \"print the status as one JSON object per line\")\n43   \tflags.Parse(args)\n```", &tools.Result{})
		}},
		{"write-new", func(s toolStage) {
			s.call("w1", "write", `{"path":"docs/notes.md","content":"# Notes\n\nFirst line.\nSecond line.\n"}`, 20*time.Millisecond,
				"Wrote 34 bytes to docs/notes.md", &tools.Result{})
		}},
		{"failed", func(s toolStage) {
			s.call("e2", "Edit", `{"file_path":"main.go","old_string":"nope","new_string":"x"}`, 10*time.Millisecond,
				"Error: old_string not found in main.go", &tools.Result{Failed: true})
		}},
	}},
	{"read", []toolVariant{
		{"read", func(s toolStage) {
			s.call("r1", "read", `{"path":"internal/tui/view.go","offset":10,"limit":40}`, 5*time.Millisecond,
				numberedLines("line", 40), &tools.Result{})
		}},
		{"failed", func(s toolStage) {
			s.call("r2", "read", `{"path":"missing.go"}`, 2*time.Millisecond,
				"Error: open missing.go: no such file or directory", &tools.Result{Failed: true})
		}},
	}},
	{"search", []toolVariant{
		{"grep", func(s toolStage) {
			s.call("g1", "grep", `{"pattern":"asJSON","path":"cmd"}`, 12*time.Millisecond,
				"cmd/kn/status.go:42: asJSON := flags.Bool\ncmd/kn/status.go:57: if *asJSON {", &tools.Result{})
		}},
		{"glob", func(s toolStage) {
			s.call("g2", "glob", `{"pattern":"**/*_test.go"}`, 8*time.Millisecond,
				numberedLines("internal/tui/file", 9), &tools.Result{})
		}},
		{"group", func(s toolStage) {
			s.call("r1", "read", `{"path":"cmd/kn/status.go"}`, 5*time.Millisecond, numberedLines("line", 12), &tools.Result{})
			s.call("r2", "read", `{"path":"cmd/kn/main.go"}`, 4*time.Millisecond, numberedLines("line", 30), &tools.Result{})
			s.call("g1", "grep", `{"pattern":"asJSON"}`, 9*time.Millisecond, "cmd/kn/status.go:42: asJSON", &tools.Result{})
		}},
		{"group-expanded", func(s toolStage) {
			s.call("r1", "read", `{"path":"cmd/kn/status.go"}`, 5*time.Millisecond, numberedLines("line", 12), &tools.Result{})
			s.call("g1", "glob", `{"pattern":"*.md"}`, 3*time.Millisecond, "README.md\nCHANGELOG.md", &tools.Result{})
			s.m.blocks[len(s.m.blocks)-1].toggle()
		}},
	}},
	{"fetch", []toolVariant{
		{"web-fetch", func(s toolStage) {
			s.call("f1", "web_fetch", `{"url":"https://pkg.go.dev/github.com/charmbracelet/bubbletea"}`, 640*time.Millisecond,
				"url=https://pkg.go.dev/github.com/charmbracelet/bubbletea\nstatus=200\ncontent_type=text/html\n\n"+strings.Repeat("Package tea provides a framework for building terminal apps. ", 20), &tools.Result{})
		}},
		{"hosted-search", func(s toolStage) {
			s.m.Update(hostedSearchMsg{ID: "ws1", Status: "searching", Queries: []string{"bubbletea viewport"}})
			s.m.Update(hostedSearchMsg{ID: "ws1", Status: "completed", Queries: []string{"bubbletea viewport"}, Sources: []ai.SearchSource{
				{URL: "https://github.com/charmbracelet/bubbles", Title: "Bubbles: TUI components"},
				{URL: "https://pkg.go.dev/github.com/charmbracelet/bubbles/viewport", Title: "viewport package"},
				{URL: "https://www.example.com/blog/tui", Title: ""},
				{URL: "https://example.org/a", Title: "Another source"},
				{URL: "https://example.org/b", Title: "Yet another"},
			}})
		}},
		{"hosted-search-failed", func(s toolStage) {
			s.m.Update(hostedSearchMsg{ID: "ws2", Status: "failed", Queries: []string{"kn release notes"}, Error: "search quota exceeded"})
		}},
	}},
	{"mcp", []toolVariant{
		{"ok", func(s toolStage) {
			s.call("m1", "mcp__github__search_issues", `{"query":"is:open label:tui","limit":20}`, 900*time.Millisecond,
				numberedLines("#", 14), &tools.Result{})
		}},
		{"failed", func(s toolStage) {
			s.call("m2", "mcp__docs__lookup", `{"id":42}`, 100*time.Millisecond,
				"Error: MCP server docs is not connected", &tools.Result{Failed: true})
		}},
	}},
	{"generic", []toolVariant{
		{"ok", func(s toolStage) {
			s.call("x1", "browser_exec", `{"script":"document.title"}`, 1200*time.Millisecond,
				"Example Domain", &tools.Result{Parts: []ai.ContentPart{{Type: "image_url", MimeType: "image/png", W: 1280, H: 720}}})
		}},
		{"hook-denied", func(s toolStage) {
			s.call("x2", "bash", `{"command":"rm -rf build"}`, 5*time.Millisecond,
				"Error: hook PreToolUse denied tool call: destructive commands need review", &tools.Result{Failed: true})
		}},
		{"hook-failed", func(s toolStage) {
			s.call("x3", "custom_tool", `{"input":"x"}`, 50*time.Millisecond,
				"done\nHook PostToolUse failed: exit status 1", &tools.Result{})
		}},
	}},
}

func renderToolStage(t *testing.T, mode goldenMode, width int, build func(s toolStage)) string {
	t.Helper()
	restore := useProfile(t, mode.profile, mode.dark)
	defer restore()
	SetLightTheme(!mode.dark)
	if mode.mono {
		setMonoMode(true, true)
	}
	setASCIIGlyphs(mode.ascii)
	clock := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	toolTimer.now = func() time.Time { return clock }
	m := compactCmdModel()
	m.now = func() time.Time { return clock }
	m.applyAppearance()
	m.Update(mkWinSize(width, 40))
	build(toolStage{m: m, clock: &clock})
	var rows []string
	for i := range m.blocks {
		if m.blocks[i].kind == blockUser || m.blocks[i].kind == blockBanner {
			continue
		}
		r := m.blocks[i].render(width)
		for j, l := range strings.Split(r, "\n") {
			if w := lipgloss.Width(l); w > width {
				t.Errorf("block %d row %d is %d cells wide at %d columns: %q", i, j, w, width, ansi.Strip(l))
			}
		}
		rows = append(rows, r)
	}
	return strings.Join(rows, "\n")
}

func TestToolViewGoldens(t *testing.T) {
	prevNow := toolTimer.now
	defer func() { toolTimer.now = prevNow }()
	defer SetLightTheme(false)
	modes := []goldenMode{goldenModes[0], goldenModes[4], goldenModes[5]} // truecolor-dark, nocolor, ascii
	for _, fam := range toolViewFamilies {
		for _, width := range []int{40, 120} {
			for _, mode := range modes {
				name := fmt.Sprintf("tools_%s_%s_%d", fam.name, mode.name, width)
				t.Run(name, func(t *testing.T) {
					var raw, txt strings.Builder
					for _, v := range fam.variants {
						out := renderToolStage(t, mode, width, v.build)
						head := "== " + v.name + " ==\n"
						raw.WriteString(head + out + "\n")
						txt.WriteString(head + stripView(out))
					}
					if mode.ascii {
						for _, r := range txt.String() {
							if r > 0x7e {
								t.Errorf("ascii view contains %q", r)
								break
							}
						}
					}
					if mode.mono || mode.ascii {
						checkGolden(t, name+".txt", txt.String())
						return
					}
					checkGolden(t, name+".ansi", raw.String())
					checkGolden(t, name+".txt", txt.String())
				})
			}
		}
	}
}

// ── Diffs ────────────────────────────────────────────────────────────────

const sampleDiff = "10   func main() {\n11 - \tfmt.Println(\"hello\")\n11 + \tfmt.Println(\"hello, world\", strings.Repeat(\"=\", 40), os.Args[1:])\n12   }\n… +3 more lines"

func TestRenderDiffGoldens(t *testing.T) {
	defer SetLightTheme(false)
	for _, mode := range []goldenMode{goldenModes[0], goldenModes[1], goldenModes[4]} {
		t.Run(mode.name, func(t *testing.T) {
			restore := useProfile(t, mode.profile, mode.dark)
			defer restore()
			SetLightTheme(!mode.dark)
			if mode.mono {
				setMonoMode(true, true)
			}
			var raw, txt strings.Builder
			for _, c := range []struct {
				name  string
				width int
				out   string
			}{
				{"highlighted", 120, renderDiff(sampleDiff, "main.go", 120)},
				{"wrapped", 40, renderDiff(sampleDiff, "main.go", 40)},
				{"folded", 80, renderDiffFolded(sampleDiff, "main.go", 80, 2)},
				{"unnumbered", 80, renderDiff("+ added\n- removed\n  same", "x.txt", 80)},
			} {
				for _, l := range strings.Split(c.out, "\n") {
					if w := lipgloss.Width(l); w > c.width {
						t.Errorf("%s: row %d cells wide at %d: %q", c.name, w, c.width, ansi.Strip(l))
					}
				}
				head := "== " + c.name + " ==\n"
				raw.WriteString(head + c.out + "\n")
				txt.WriteString(head + stripView(c.out))
			}
			if !mode.mono {
				checkGolden(t, "diff_"+mode.name+".ansi", raw.String())
			}
			checkGolden(t, "diff_"+mode.name+".txt", txt.String())
		})
	}
}

func TestRenderDiffWrapsInsteadOfTruncating(t *testing.T) {
	out := ansi.Strip(renderDiff(sampleDiff, "main.go", 40))
	if strings.Contains(out, "…\n") {
		t.Fatalf("a long diff row should wrap, not be cut:\n%s", out)
	}
	compact := strings.Join(strings.Fields(out), "")
	if !strings.Contains(compact, `os.Args[1:])`) {
		t.Fatalf("wrapped row lost its tail:\n%s", out)
	}
	folded := ansi.Strip(renderDiffFolded(sampleDiff, "main.go", 80, 2))
	if !strings.Contains(folded, glyphs.Fold+" +3 lines · ctrl+e") || strings.Contains(folded, "12  ") {
		t.Fatalf("folded diff wrong:\n%s", folded)
	}
}

func TestRenderDiffHighlightsByExtension(t *testing.T) {
	restore := useProfile(t, goldenModes[0].profile, true)
	defer restore()
	goSide := renderDiff("1 + func main() {}", "main.go", 80)
	txtSide := renderDiff("1 + func main() {}", "main.txt", 80)
	if goSide == txtSide {
		t.Fatal("a .go diff should be highlighted differently from plain text")
	}
}

// ── Header outcomes ──────────────────────────────────────────────────────

func headerOf(m *model, id string) string {
	for i := range m.blocks {
		if m.blocks[i].kind == blockToolRun && m.blocks[i].toolID == id {
			return ansi.Strip(m.blocks[i].render(m.width))
		}
	}
	return ""
}

func TestToolResultMsgSetsExitBadge(t *testing.T) {
	prevNow := toolTimer.now
	defer func() { toolTimer.now = prevNow }()
	clock := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	toolTimer.now = func() time.Time { return clock }
	m := compactCmdModel()
	m.Update(mkWinSize(100, 30))
	s := toolStage{m: m, clock: &clock}
	s.call("b1", "bash", `{"command":"false"}`, time.Second, "(exit: exit status 3)", &tools.Result{Failed: true, ExitCode: exitCode(3), Duration: 2800 * time.Millisecond})
	h := headerOf(m, "b1")
	if !strings.Contains(h, "exit 3 · 2.8s") || !strings.HasPrefix(h, glyphs.Fail+" Bash") {
		t.Fatalf("header should carry the exit badge and the tool's own time: %q", h)
	}
	s.call("b2", "bash", `{"command":"sleep 9"}`, time.Second, "(command timed out)", &tools.Result{Failed: true, TimedOut: true})
	if h := headerOf(m, "b2"); !strings.Contains(h, "timed out · 1.0s") {
		t.Fatalf("timed-out header: %q", h)
	}
	res := ansi.Strip(m.blocks[len(m.blocks)-1].render(m.width))
	if strings.Contains(res, "command timed out") {
		t.Fatalf("the badge says it; the body should not repeat the note: %q", res)
	}
}

func TestRunningToolShowsLiveTimer(t *testing.T) {
	prevNow := toolTimer.now
	defer func() { toolTimer.now = prevNow }()
	clock := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	toolTimer.now = func() time.Time { return clock }
	m := compactCmdModel()
	m.Update(mkWinSize(100, 30))
	_, cmd := m.Update(toolStartMsg{id: "b1", name: "bash", args: `{"command":"go test ./..."}`})
	if cmd == nil {
		t.Fatal("a running call should schedule the elapsed-time tick")
	}
	clock = clock.Add(5 * time.Second)
	if _, cmd := m.Update(toolTickMsg{}); cmd == nil {
		t.Fatal("the tick should continue while the call runs")
	}
	if h := headerOf(m, "b1"); !strings.Contains(h, "5s") {
		t.Fatalf("running header should show the elapsed time: %q", h)
	}
	m.Update(toolEndMsg{id: "b1", name: "bash", result: "ok"})
	if _, cmd := m.Update(toolTickMsg{}); cmd != nil {
		t.Fatal("the tick should stop once nothing runs")
	}
}

func TestTurnEndSettlesRunningTools(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(100, 30))
	m.Update(toolStartMsg{id: "b1", name: "bash", args: `{"command":"sleep 100"}`})
	m.settleRunningTools()
	if h := headerOf(m, "b1"); !strings.Contains(h, "interrupted") || m.blocks[len(m.blocks)-1].toolRunning {
		t.Fatalf("a call left running should settle as interrupted: %q", h)
	}
}

// ── Resumed sessions ─────────────────────────────────────────────────────

func TestSeededToolsUseRunMeta(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(100, 30))
	call := ai.Message{Role: "assistant"}
	var bash, edit ai.ToolCall
	bash.ID, bash.Function.Name, bash.Function.Arguments = "c1", "bash", `{"command":"make test"}`
	bash.ExitStatus, bash.ExitCode, bash.DurationMs = exitCode(2), 2, 3200
	edit.ID, edit.Function.Name, edit.Function.Arguments = "c2", "edit", `{"path":"a.go"}`
	edit.DurationMs = 400
	call.ToolCalls = []ai.ToolCall{bash, edit}
	msgs := []ai.Message{
		call,
		{Role: "tool", Name: "bash", ToolCallID: "c1", Content: "FAIL\n(exit: exit status 2)"},
		{Role: "tool", Name: "edit", ToolCallID: "c2", Content: "Replaced 1 occurrence(s) in a.go\n```diff\n4 - x\n4 + y\n```"},
	}
	m.seedTranscript(msgs, 1)
	if h := headerOf(m, "c1"); !strings.Contains(h, "exit 2 · 3.2s") {
		t.Fatalf("resumed bash header should use the recorded run: %q", h)
	}
	if h := headerOf(m, "c2"); !strings.Contains(h, "+1 "+glyphs.Minus+"1  "+glyphs.OK+" 0.4s") {
		t.Fatalf("resumed edit header should carry its counts and time: %q", h)
	}
}

// ── Grouping ─────────────────────────────────────────────────────────────

func TestConsecutiveReadsFoldIntoGroup(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(100, 30))
	clock := time.Now()
	s := toolStage{m: m, clock: &clock}
	s.call("r1", "read", `{"path":"a.go"}`, 0, "1\n2", &tools.Result{})
	s.call("r2", "read", `{"path":"b.go"}`, 0, "1\n2", &tools.Result{})
	s.call("g1", "grep", `{"pattern":"TODO"}`, 0, "a.go:1: TODO", &tools.Result{})
	s.call("g2", "glob", `{"pattern":"*.go"}`, 0, "a.go\nb.go", &tools.Result{})
	groups := 0
	var g *block
	for i := range m.blocks {
		if toolGroupBlock(&m.blocks[i]) {
			groups++
			g = &m.blocks[i]
		}
	}
	if groups != 1 || len(g.viewMeta().group) != 4 {
		t.Fatalf("four reads should fold into one group, got %d groups", groups)
	}
	out := ansi.Strip(g.render(m.width))
	if !strings.Contains(out, "Read 2 files, searched 2 patterns") || !strings.Contains(out, "a.go, b.go, TODO") {
		t.Fatalf("group summary: %q", out)
	}
	g.toggle()
	out = ansi.Strip(g.render(m.width))
	for _, want := range []string{"Read  a.go", "Read  b.go", "Grep  TODO", "Glob  *.go"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expanded group missing %q:\n%s", want, out)
		}
	}

	// A failure, or another tool, ends the run.
	s.call("b1", "bash", `{"command":"ls"}`, 0, "x", &tools.Result{ExitCode: exitCode(0)})
	s.call("r3", "read", `{"path":"c.go"}`, 0, "Error: no such file", &tools.Result{Failed: true})
	s.call("r4", "read", `{"path":"d.go"}`, 0, "1", &tools.Result{})
	for _, b := range m.blocks[len(m.blocks)-4:] {
		if toolGroupBlock(&b) {
			t.Fatal("a failed read should not join a group")
		}
	}
}

func TestSeededReadsFoldIntoGroup(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(100, 30))
	call := ai.Message{Role: "assistant"}
	var msgs []ai.Message
	for i, p := range []string{"a.go", "b.go", "c.go"} {
		var tc ai.ToolCall
		tc.ID, tc.Function.Name, tc.Function.Arguments = fmt.Sprintf("r%d", i), "read", `{"path":"`+p+`"}`
		call.ToolCalls = append(call.ToolCalls, tc)
		msgs = append(msgs, ai.Message{Role: "tool", Name: "read", ToolCallID: tc.ID, Content: "1\n2\n3"})
	}
	m.seedTranscript(append([]ai.Message{call}, msgs...), 1)
	var out strings.Builder
	for i := range m.blocks {
		out.WriteString(ansi.Strip(m.blocks[i].render(m.width)) + "\n")
	}
	if !strings.Contains(out.String(), "Read 3 files") {
		t.Fatalf("resumed reads should group as they did live:\n%s", out.String())
	}
}

// ── Todo pane ────────────────────────────────────────────────────────────

func todoList(statuses ...string) []agent.Todo {
	out := make([]agent.Todo, len(statuses))
	for i, s := range statuses {
		out[i] = agent.Todo{ID: fmt.Sprintf("t%d", i+1), Content: fmt.Sprintf("Step number %d", i+1), Status: s}
	}
	return out
}

func TestTodoPaneShowsPlan(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(80, 40))
	if m.todoPaneView() != "" || m.todoPaneRows() != 0 {
		t.Fatal("no plan, no pane")
	}
	m.agent.Todos = todoList("completed", "completed", "in_progress", "pending", "pending", "pending", "pending")
	pane := ansi.Strip(m.todoPaneView())
	for _, want := range []string{"Plan  2/7", glyphs.OK + " Step number 2", "◐ Step number 3", "○ Step number 4", glyphs.Fold + " 1 more"} {
		if !strings.Contains(pane, want) {
			t.Fatalf("pane missing %q:\n%s", want, pane)
		}
	}
	if strings.Contains(pane, "Step number 1") {
		t.Fatalf("the pane should start one before the current step:\n%s", pane)
	}
	if got, want := m.todoPaneRows(), strings.Count(pane, "\n")+2; got != want {
		t.Fatalf("rows = %d, want %d", got, want)
	}
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "Plan  2/7") {
		t.Fatalf("the pane should be drawn above the composer:\n%s", view)
	}
	if lines := strings.Split(view, "\n"); len(lines) > 40 {
		t.Fatalf("view is %d rows on a 40-row screen", len(lines))
	}

	// Short terminals get one row naming the current step.
	m.Update(mkWinSize(80, 16))
	if pane := ansi.Strip(m.todoPaneView()); strings.Contains(pane, "\n") || !strings.Contains(pane, "Plan  2/7 · Step number 3") {
		t.Fatalf("short pane: %q", pane)
	}

	// All done: hidden between turns, kept for the rest of a turn.
	m.agent.Todos = todoList("completed", "cancelled")
	if m.todoPaneView() != "" {
		t.Fatal("a finished plan should hide between turns")
	}
}

func TestTodoPaneFollowsTodosMsg(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(80, 40))
	m.busy = true
	m.Update(todosMsg{agent: m.agent, items: todoList("in_progress", "pending")})
	if pane := ansi.Strip(m.todoPaneView()); !strings.Contains(pane, "Plan  0/2") {
		t.Fatalf("pane should follow the reported plan: %q", pane)
	}
	m.Update(todosMsg{agent: m.agent, items: todoList("completed", "completed")})
	if pane := ansi.Strip(m.todoPaneView()); !strings.Contains(pane, "Plan  2/2") {
		t.Fatalf("a finished plan stays for the rest of the turn: %q", pane)
	}
	// A message from a replaced agent is ignored.
	m.Update(todosMsg{agent: &agent.Agent{}, items: todoList("pending")})
	if pane := ansi.Strip(m.todoPaneView()); !strings.Contains(pane, "Plan  2/2") {
		t.Fatalf("stale agent's plan should be ignored: %q", pane)
	}
}

func TestTodoCallsHiddenFromTranscript(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(80, 30))
	n := len(m.blocks)
	m.Update(toolCallMsg{id: "t1", name: "todowrite", args: `{"todos":[]}`})
	m.Update(toolStartMsg{id: "t1", name: "todowrite", args: `{"todos":[]}`})
	m.Update(toolEndMsg{id: "t1", name: "todowrite", result: "Plan updated: 2 item(s), 2 open."})
	if len(m.blocks) != n {
		t.Fatalf("a todowrite call should not add transcript rows, got %d new", len(m.blocks)-n)
	}
	m.Update(toolEndMsg{id: "t2", name: "todowrite", result: "Error: keep exactly one item in_progress (2 given)"})
	if len(m.blocks) == n {
		t.Fatal("a failed todowrite call should show")
	}

	// Resumed: hidden too.
	m2 := compactCmdModel()
	m2.Update(mkWinSize(80, 30))
	call := ai.Message{Role: "assistant"}
	var tc ai.ToolCall
	tc.ID, tc.Function.Name, tc.Function.Arguments = "t1", "todowrite", `{"todos":[]}`
	call.ToolCalls = []ai.ToolCall{tc}
	m2.seedTranscript([]ai.Message{call, {Role: "tool", Name: "todowrite", ToolCallID: "t1", Content: "Plan cleared."}}, 1)
	for i := range m2.blocks {
		if m2.blocks[i].toolName == "todowrite" {
			t.Fatal("a resumed todowrite call should stay hidden")
		}
	}
}

func TestTodoPaneASCII(t *testing.T) {
	restore := useProfile(t, goldenModes[5].profile, true)
	defer restore()
	setASCIIGlyphs(true)
	m := compactCmdModel()
	m.Update(mkWinSize(80, 40))
	m.agent.Todos = todoList("completed", "in_progress", "pending", "cancelled")
	pane := ansi.Strip(m.todoPaneView())
	for _, want := range []string{"[x] Step number 1", "[>] Step number 2", "[ ] Step number 3", "[-] Step number 4"} {
		if !strings.Contains(pane, want) {
			t.Fatalf("ascii pane missing %q:\n%s", want, pane)
		}
	}
	for _, r := range pane {
		if r > 0x7e {
			t.Fatalf("ascii pane contains %q", r)
		}
	}
}

// ── Chips and hooks ──────────────────────────────────────────────────────

func TestImageChipsAndHookNotes(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(100, 30))
	clock := time.Now()
	s := toolStage{m: m, clock: &clock}
	s.call("i1", "Image", `{"path":"shot.png"}`, 0, "Read image shot.png", &tools.Result{Parts: []ai.ContentPart{{Type: "image_url", MimeType: "image/png", W: 640, H: 480}}})
	res := ansi.Strip(m.blocks[len(m.blocks)-1].render(m.width))
	if !strings.Contains(res, "[image 1 · png · 640×480]") {
		t.Fatalf("result should carry an image chip:\n%s", res)
	}
	s.call("h1", "bash", `{"command":"ls"}`, 0, "a\nHook PostToolUse failed: exit status 1", &tools.Result{ExitCode: exitCode(0)})
	res = ansi.Strip(m.blocks[len(m.blocks)-1].render(m.width))
	if !strings.Contains(res, glyphs.Warn+" PostToolUse hook failed: exit status 1") {
		t.Fatalf("hook failure should be an annotation:\n%s", res)
	}
}
