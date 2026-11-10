package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/Stack-Cairn/K-brain/internal/agent"
)

// topPanelModel is a model with a goal, a reported plan and two finished
// subagents, on a fixed clock.
func topPanelModel(t *testing.T, w, h int) (*model, *time.Time) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	clock := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	m := compactCmdModel()
	m.now = func() time.Time { return clock }
	m.applyAppearance()
	m.Update(mkWinSize(w, h))
	m.setGoal("Ship the v1.4 release checklist")
	clock = clock.Add(62 * time.Second)
	m.goalRounds = 1
	m.busy = true
	m.panelTurnStarted(true)
	m.agent.Todos = todoList("completed", "in_progress", "pending", "pending")
	m.Update(todosMsg{agent: m.agent, items: m.agent.Todos})
	for i, d := range []string{"Audit changelog entries", "Verify build matrix"} {
		status := agent.TaskDone
		if i == 1 {
			status = agent.TaskError
		}
		m.agent.RestoreTask(agent.BackgroundTask{
			ID: fmt.Sprintf("sub-%d", i+1), Description: d, Prompt: "p", Status: status,
			StartedAt: clock.Add(-9 * time.Second), EndedAt: clock,
		})
	}
	return m, &clock
}

func panelLines(m *model) []string {
	m.layout()
	v := m.topPanelView()
	if v == "" {
		return nil
	}
	return strings.Split(ansi.Strip(v), "\n")
}

func TestTopPanelHiddenWithoutWork(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(80, 30))
	m.layout()
	if v := m.topPanelView(); v != "" || m.tp.rows != 0 || m.topPanelShown() {
		t.Fatalf("no goal, plan or subagent: no panel, got %q (%d rows)", v, m.tp.rows)
	}
	if strings.Contains(stripAll(m.View()), "Alt+T") {
		t.Fatal("the empty panel must not draw its header")
	}
}

func TestTopPanelCollapsedIsOneSummaryRow(t *testing.T) {
	m, _ := topPanelModel(t, 120, 40)
	m.tp.expanded, m.tp.userSet = false, true
	lines := panelLines(m)
	if len(lines) != 1 || m.tp.rows != 1 {
		t.Fatalf("collapsed panel should be one row, got %d:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	for _, want := range []string{"▸", "Goal", "Executing", "Ship the v1.4", "round 2/", "1m02s", "Plan 1/4", "1 error · 1 done", "Alt+T expand"} {
		if !strings.Contains(lines[0], want) {
			t.Fatalf("collapsed header missing %q: %q", want, lines[0])
		}
	}
	// The footer no longer repeats what the panel says.
	if stats := m.footerStats(); strings.Contains(stats, "sub") || strings.Contains(stats, "Ship the") {
		t.Fatalf("footer repeats the panel: %q", stats)
	}
}

func TestTopPanelExpandedListsEachItem(t *testing.T) {
	m, _ := topPanelModel(t, 120, 40)
	m.busy = false
	m.tp.expanded, m.tp.userSet = true, true
	lines := panelLines(m)
	text := strings.Join(lines, "\n")
	for _, want := range []string{"▾", "Alt+T collapse", "◎ Ship the v1.4 release checklist", "Idle · round 2/", "☰ Plan 1/4", "◐ Step number 2", "sub-1", "Audit changelog entries", "sub-2", "Verify build matrix"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expanded panel missing %q:\n%s", want, text)
		}
	}
	if len(lines) > m.topPanelBudget() || m.tp.rows != len(lines) {
		t.Fatalf("panel took %d rows (measured %d), budget %d", len(lines), m.tp.rows, m.topPanelBudget())
	}
	if last := lines[len(lines)-1]; strings.Trim(last, "─") != "" {
		t.Fatalf("the panel should end on a rule, got %q", last)
	}
	// The panel sits above the transcript and the composer.
	v := stripAll(m.View())
	if p, c := strings.Index(v, "Verify build matrix"), strings.Index(v, "Ask k-brain"); p < 0 || c < 0 || p > c {
		t.Fatalf("panel rows must render above the composer:\n%s", v)
	}
	if !strings.HasPrefix(v, "── ▾") {
		t.Fatalf("the panel header should be the first row:\n%s", v)
	}
}

func TestTopPanelPlanStepsFillTheRoomWithoutSubagents(t *testing.T) {
	m := compactCmdModel()
	t.Setenv("HOME", t.TempDir())
	m.Update(mkWinSize(80, 40))
	m.busy = true
	m.panelTurnStarted(true)
	m.Update(todosMsg{agent: m.agent, items: todoList("completed", "completed", "in_progress", "pending", "pending", "pending", "pending", "pending", "pending", "pending", "pending")})
	text := strings.Join(panelLines(m), "\n")
	for _, want := range []string{"Plan 2/11", "✓ Step number 2", "◐ Step number 3", "○ Step number 4"} {
		if !strings.Contains(text, want) {
			t.Fatalf("plan steps missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Step number 1\n") {
		t.Fatalf("the steps start one before the current one:\n%s", text)
	}
	if m.tp.rows > m.topPanelBudget() {
		t.Fatalf("plan steps overflow the budget: %d > %d", m.tp.rows, m.topPanelBudget())
	}
}

func TestTopPanelDegenerateSizes(t *testing.T) {
	for _, size := range [][2]int{{1, 1}, {10, 5}, {23, 30}, {24, 5}, {24, 6}, {40, 11}, {40, 12}, {80, 24}, {200, 60}} {
		w, h := size[0], size[1]
		t.Run(fmt.Sprintf("%dx%d", w, h), func(t *testing.T) {
			m, _ := topPanelModel(t, w, h)
			m.tp.expanded, m.tp.userSet = true, true
			m.layout()
			panel := m.topPanelView()
			switch {
			case w < topPanelMinWidth || h < topPanelMinHeight:
				if panel != "" || m.tp.rows != 0 {
					t.Fatalf("too small for the panel, got %d rows", m.tp.rows)
				}
			case h < topPanelExpandHeight:
				if m.tp.rows != 1 {
					t.Fatalf("a short terminal keeps the panel to its header, got %d rows", m.tp.rows)
				}
			default:
				if m.tp.rows < 3 || m.tp.rows > m.topPanelBudget() {
					t.Fatalf("expanded rows %d outside 3..%d", m.tp.rows, m.topPanelBudget())
				}
			}
			for i, l := range strings.Split(panel, "\n") {
				if lipgloss.Width(l) > w {
					t.Fatalf("panel row %d is %d cells on a %d-column screen: %q", i, lipgloss.Width(l), w, ansi.Strip(l))
				}
			}
			v := m.View()
			if n := strings.Count(v, "\n") + 1; n > h {
				t.Fatalf("frame is %d rows on a %d-row screen", n, h)
			}
			if h >= 12 && !strings.Contains(stripAll(v), "Ask k-brain") && !strings.Contains(stripAll(v), "❯") {
				t.Fatalf("the composer must stay on screen:\n%s", stripAll(v))
			}
		})
	}
}

func TestTopPanelToggleByKeyAndClick(t *testing.T) {
	m, _ := topPanelModel(t, 100, 30)
	m.busy = false
	m.layout()
	start := m.panelExpanded()

	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t"), Alt: true})
	if m.panelExpanded() == start || !m.tp.userSet {
		t.Fatalf("alt+t should flip the panel: was %v, now %v", start, m.panelExpanded())
	}
	if m.input.Value() != "" {
		t.Fatalf("alt+t must not type into the composer, got %q", m.input.Value())
	}
	rows := m.tp.rows

	m.View()
	m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 3, Y: m.viewTop})
	if m.panelExpanded() != start || m.tp.rows == rows {
		t.Fatalf("a click on the header should flip it back: expanded=%v rows %d→%d", m.panelExpanded(), rows, m.tp.rows)
	}

	// The choice holds for the session: new work does not reopen a folded
	// panel, and a session change keeps it.
	m.tp.expanded = false
	m.Update(todosMsg{agent: m.agent, items: todoList("in_progress")})
	m.busy = true
	m.layout()
	if m.panelExpanded() {
		t.Fatal("a panel the user folded must not reopen by itself")
	}
	m.resetTopPanel()
	if !m.tp.userSet || m.tp.expanded {
		t.Fatal("a session change keeps the user's choice")
	}
}

func TestTopPanelAutoOpensWhileWorkingAndFoldsWhenSettled(t *testing.T) {
	m := compactCmdModel()
	t.Setenv("HOME", t.TempDir())
	m.Update(mkWinSize(100, 30))
	m.busy = true
	m.panelTurnStarted(true)
	m.Update(todosMsg{agent: m.agent, items: todoList("in_progress", "pending")})
	if !m.panelExpanded() || !m.tp.autoOpened {
		t.Fatal("a plan in progress should open the panel")
	}
	m.agent.Todos = todoList("completed", "completed")
	m.Update(todosMsg{agent: m.agent, items: m.agent.Todos})
	m.busy = false
	m.layout()
	if m.panelExpanded() {
		t.Fatal("the panel should fold once the turn settles")
	}
	lines := panelLines(m)
	if len(lines) != 1 || !strings.Contains(lines[0], "Plan 2/2 ✓") {
		t.Fatalf("a finished plan folds into the header: %q", lines)
	}
}

func TestTopPanelGoalStates(t *testing.T) {
	m, _ := topPanelModel(t, 120, 40)
	for _, c := range []struct {
		busy  bool
		state string
		want  string
	}{{true, "", "Executing"}, {false, "", "Idle"}, {false, "waiting", "Waiting for you"}, {false, "paused", "Paused"}} {
		m.busy, m.tp.goalState = c.busy, c.state
		if got, _ := m.goalLabel(); got != c.want {
			t.Fatalf("busy=%v state=%q: label %q, want %q", c.busy, c.state, got, c.want)
		}
	}
	m.panelTurnStarted(false)
	if m.tp.goalState != "" {
		t.Fatal("a new round clears the waiting/paused state")
	}
	m.setGoal("")
	m.agent.Todos, m.tp.planSeq = nil, 0
	m.agent.Tasks().ClearSettled()
	m.layout()
	if m.topPanelShown() {
		t.Fatal("clearing the goal with nothing else to show hides the panel")
	}
}

func TestTopPanelPlanLifecycle(t *testing.T) {
	newModel := func() *model {
		m := compactCmdModel()
		t.Setenv("HOME", t.TempDir())
		m.Update(mkWinSize(100, 30))
		m.busy = true
		m.panelTurnStarted(true)
		return m
	}
	report := func(m *model, statuses ...string) {
		items := todoList(statuses...)
		m.agent.Todos = items
		m.Update(todosMsg{agent: m.agent, items: items})
	}

	t.Run("turn ended with open steps", func(t *testing.T) {
		m := newModel()
		report(m, "completed", "in_progress", "pending")
		m.busy = false
		lines := panelLines(m)
		if len(lines) != 1 || !strings.Contains(lines[0], "Plan 1/3") {
			t.Fatalf("an open plan folds to its chip between turns: %q", lines)
		}
	})
	t.Run("turn cancelled", func(t *testing.T) {
		m := newModel()
		report(m, "in_progress", "pending")
		m.busy = false // the turn ends without the plan being finished
		m.layout()
		if m.panelExpanded() {
			t.Fatal("a cancelled turn's plan must not stay open")
		}
	})
	t.Run("all done then the user moves on", func(t *testing.T) {
		m := newModel()
		report(m, "completed", "completed")
		m.busy = false
		if lines := panelLines(m); len(lines) != 1 || !strings.Contains(lines[0], "Plan 2/2 ✓") {
			t.Fatalf("a finished plan stays reachable as a chip: %q", lines)
		}
		m.busy = true
		m.panelTurnStarted(true)
		if m.panelPlan() != nil {
			t.Fatal("a finished plan must not come back on the next message")
		}
		m.busy = false
		if plan := m.panelPlan(); plan != nil {
			t.Fatalf("the finished plan resurfaced between turns: %+v", plan)
		}
	})
	t.Run("a machine turn keeps the finished plan", func(t *testing.T) {
		m := newModel()
		report(m, "completed")
		m.panelTurnStarted(false)
		if m.panelPlan() == nil {
			t.Fatal("a goal round or subagent notice is not the user moving on")
		}
	})
	t.Run("new session", func(t *testing.T) {
		m := newModel()
		report(m, "in_progress", "pending")
		m.busy = false
		m.command("/new")
		if len(m.agent.Todos) != 0 || m.panelPlan() != nil || m.topPanelShown() {
			t.Fatalf("/new should drop the plan from the agent and the panel: todos=%d", len(m.agent.Todos))
		}
	})
	t.Run("restored session", func(t *testing.T) {
		m := newModel()
		m.busy = false
		m.resetTopPanel()
		m.agent.Todos = todoList("in_progress", "pending")
		if m.panelPlan() != nil || m.topPanelShown() {
			t.Fatal("a plan this run never reported must not show")
		}
	})
}

func TestTopPanelASCII(t *testing.T) {
	setASCIIGlyphs(true)
	defer setASCIIGlyphs(false)
	m, _ := topPanelModel(t, 100, 30)
	m.tp.expanded, m.tp.userSet = true, true
	for _, l := range panelLines(m) {
		for _, r := range l {
			if r > 0x7e {
				t.Fatalf("ascii panel row contains %q: %q", r, l)
			}
		}
	}
}

func TestTopPanelGoldens(t *testing.T) {
	defer SetLightTheme(false)
	for _, mode := range []goldenMode{goldenModes[0], goldenModes[5]} {
		for _, size := range [][2]int{{80, 24}, {120, 40}} {
			for _, expanded := range []bool{false, true} {
				state := "collapsed"
				if expanded {
					state = "expanded"
				}
				name := fmt.Sprintf("top-panel-%s_%s_%dx%d", state, mode.name, size[0], size[1])
				t.Run(name, func(t *testing.T) {
					restore := useProfile(t, mode.profile, mode.dark)
					defer restore()
					SetLightTheme(!mode.dark)
					setASCIIGlyphs(mode.ascii)
					defer setASCIIGlyphs(false)
					m, _ := topPanelModel(t, size[0], size[1])
					m.busy = false
					m.tp.expanded, m.tp.userSet = expanded, true
					m.applyAppearance()
					m.layout()
					view := m.topPanelView()
					checkGolden(t, name+".ansi", view)
					checkGolden(t, name+".txt", stripView(view))
				})
			}
		}
	}
}

func TestGoalCheckPromptIsOneQuietRow(t *testing.T) {
	m, _ := topPanelModel(t, 100, 30)
	m.busy = false
	n := len(m.blocks)
	prompt := goalContinuePrompt(m.goal)
	row, ok := m.goalCheckRow(prompt, false)
	if !ok {
		t.Fatal("the continue prompt should be recognised")
	}
	if got := stripAll(row); !strings.Contains(got, "goal check") || !strings.Contains(got, "round 2/") || strings.Contains(got, "GOAL_MET") {
		t.Fatalf("goal check row = %q", got)
	}
	if _, ok := m.goalCheckRow(prompt, true); ok {
		t.Fatal("an authored message is never folded")
	}
	m.submitTurn(prompt, false)
	added := m.blocks[n:]
	for _, b := range added {
		if b.kind == blockUser || strings.Contains(b.text, "GOAL_NEEDS_INPUT") {
			t.Fatalf("the continue prompt leaked into the transcript as %v: %q", b.kind, stripAll(b.text))
		}
	}
}

func TestSubagentResultFoldsToOneLine(t *testing.T) {
	raw := `Error: 400 Bad Request: {"error":{"message":"scripted failure: upstream rejected the request","type":"invalid_request_error"}}`
	if got := subagentErrorSummary(raw); got != "400 Bad Request · scripted failure: upstream rejected the request" {
		t.Fatalf("error summary = %q", got)
	}
	if got := subagentErrorSummary("Error: context canceled"); got != "context canceled" {
		t.Fatalf("plain error summary = %q", got)
	}
	b := block{kind: blockTool, toolName: "subagent", text: raw}
	out := stripAll(b.render(100))
	if strings.Contains(out, `"type"`) || !strings.Contains(out, "scripted failure") || !strings.Contains(out, "ctrl+e") {
		t.Fatalf("failed subagent result should fold to its message:\n%s", out)
	}
	b.expanded = true
	if out := stripAll(b.render(100)); !strings.Contains(out, `"invalid_request_error"`) {
		t.Fatalf("ctrl+e should show the whole result:\n%s", out)
	}
	report := block{kind: blockTool, toolName: "subagent", text: "Changelog audited: 3 entries fixed.\n\n- a\n- b"}
	out = stripAll(report.render(100))
	if strings.Count(out, "\n") != 1 || !strings.Contains(out, "Changelog audited") || !strings.Contains(out, "+3 lines") {
		t.Fatalf("report should fold to its first line and a hint:\n%s", out)
	}
	one := block{kind: blockTool, toolName: "subagent", text: "done"}
	if out := stripAll(one.render(100)); strings.Contains(out, "ctrl+e") {
		t.Fatalf("a one-line report needs no fold hint: %q", out)
	}
}

func TestChildViewThinkingAndReport(t *testing.T) {
	var buf strings.Builder
	prev := -1
	for _, e := range []agent.JournaledEvent{
		{Kind: taskEventThink, S: "weighing the "}, {Kind: taskEventThink, S: "options"},
		{Kind: taskEventText, S: "Here is the report."},
	} {
		renderTaskEvent(&buf, prev, e.Kind, e.S, e.S2)
		prev = e.Kind
	}
	got := stripAll(buf.String())
	if strings.Count(got, "✻") != 1 || !strings.Contains(got, "weighing the options") || !strings.Contains(got, "options\n\nHere is the report.") {
		t.Fatalf("thinking should be one marked run before the answer: %q", got)
	}
	events := []agent.JournaledEvent{{Kind: taskEventToolStart, S: "read"}, {Kind: taskEventText, S: "Here is "}, {Kind: taskEventText, S: "the report.\n"}}
	if !reportStreamed(events, "Here is the report.") {
		t.Fatal("a report that ends the streamed text is already shown")
	}
	if reportStreamed(events[:1], "Here is the report.") || reportStreamed(events, "") {
		t.Fatal("no streamed text, or no report, is not a duplicate")
	}
}

func TestForegroundSnapshotLeavesNoSubscriber(t *testing.T) {
	r := newFgRegistry()
	r.expect("call-1", "probe")
	ctx := context.Background()
	r.ChildStart(ctx, agent.ChildInfo{ID: "foreground-1", ParentToolCallID: "call-1"}, func() {})
	for range 100 {
		_, _, live, unsubscribe := r.watch("foreground-1", agent.Events{})
		if !live {
			t.Fatal("the child is running")
		}
		unsubscribe()
	}
	_, _, _, keep := r.watch("foreground-1", agent.Events{OnText: func(string) {}})
	defer keep()
	r.mu.Lock()
	n := len(r.children["foreground-1"].subs)
	r.mu.Unlock()
	if n != 1 {
		t.Fatalf("snapshot reads left %d subscribers, want only the live one", n)
	}
	var thought strings.Builder
	_, _, _, un := r.watch("foreground-1", agent.Events{OnThink: func(s string) { thought.WriteString(s) }})
	defer un()
	ev := r.ChildEvents("foreground-1")
	ev.OnThink("hmm")
	ev.OnThink(" ok")
	if thought.String() != "hmm ok" {
		t.Fatalf("thinking should reach the open view, got %q", thought.String())
	}
	events, _, _, u2 := r.watch("foreground-1", agent.Events{})
	u2()
	if len(events) != 1 || events[0].Kind != taskEventThink || events[0].S != "hmm ok" {
		t.Fatalf("thinking should journal as one coalesced event, got %+v", events)
	}
}
