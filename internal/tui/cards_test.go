package tui

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/Stack-Cairn/K-brain/internal/permissions"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func ck(t tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: t} }

func openPerm(m *model, req tools.GateRequest) chan permAnswer {
	reply := make(chan permAnswer, 1)
	m.openPermDialog(permRequest{req: req, reply: reply}, m.modeWhy())
	return reply
}

func TestPermScopeWidensAndNarrowsWhatIsRemembered(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	m := compactCmdModel()
	m.width, m.height = 100, 40
	m.permissionMode = "normal"
	m.installPermGate()
	req := tools.GateRequest{Tool: "bash", Command: "go test ./internal/...", Shell: "bash"}
	reply := openPerm(m, req)

	view := func() string { return ansi.Strip(m.permView()) }
	if v := view(); !strings.Contains(v, "Yes, always allow Bash(go test:*)") || !strings.Contains(v, "exact · prefix · program") {
		t.Fatalf("the card starts at the suggested prefix with the scope row:\n%s", v)
	}
	m.permKey(ck(tea.KeyTab))
	if d := m.permDialog; d.sel != 1 || !reflect.DeepEqual(d.chosenRules(), []string{"Bash(go test:*)"}) {
		t.Fatalf("the first Tab selects always: sel %d rules %v", d.sel, d.chosenRules())
	}
	m.permKey(ck(tea.KeyTab))
	if got := m.permDialog.chosenRules(); !reflect.DeepEqual(got, []string{"Bash(go:*)"}) {
		t.Fatalf("Tab widens to the program, got %v", got)
	}
	m.permKey(ck(tea.KeyRight)) // already the widest: → stops there
	m.permKey(ck(tea.KeyShiftTab))
	m.permKey(ck(tea.KeyLeft))
	m.permKey(ck(tea.KeyLeft))
	if got := m.permDialog.chosenRules(); !reflect.DeepEqual(got, []string{"Bash(go test ./internal/...)"}) {
		t.Fatalf("Shift+Tab and ← narrow to the exact command, got %v", got)
	}
	if !strings.Contains(view(), "Yes, always allow Bash(go test ./internal/...)") {
		t.Fatalf("the option names the narrowed rule:\n%s", view())
	}
	m.permKey(ck(tea.KeyEnter))
	if ans := <-reply; ans.decision != tools.GateAllowAlways {
		t.Fatalf("Enter on always remembers, got %v", ans.decision)
	}
	if m.perms.decide(req, false).Effect != permissions.Allow {
		t.Fatal("the exact command is remembered")
	}
	if m.perms.decide(tools.GateRequest{Tool: "bash", Command: "go test ./cmd/...", Shell: "bash"}, false).Effect != "" {
		t.Fatal("an exact rule must not cover other arguments")
	}

	// Widened to the program, any go command is allowed.
	reply = openPerm(m, tools.GateRequest{Tool: "bash", Command: "go build ./...", Shell: "bash"})
	m.permKey(ck(tea.KeyRight)) // on "once": moves to "always"
	m.permKey(ck(tea.KeyRight)) // on "always": widens
	m.permKey(keyRunes("A"))
	<-reply
	if m.perms.decide(tools.GateRequest{Tool: "bash", Command: "go env GOPATH", Shell: "bash"}, false).Effect != permissions.Allow {
		t.Fatal("Bash(go:*) covers every go command")
	}
}

func TestPermEditScopesAndPreviewStates(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	root := t.TempDir()
	old := currentRoot
	currentRoot = func() (string, error) { return root, nil }
	t.Cleanup(func() { currentRoot = old })
	m := compactCmdModel()
	m.width, m.height = 100, 40
	m.permissionMode = "normal"
	m.installPermGate()
	path := filepath.Join(root, "cmd", "kn", "status.go")

	for _, tc := range []struct {
		name    string
		tool    string
		preview *tools.EditPreview
		title   string
		want    []string
	}{
		{"new file", "write", &tools.EditPreview{Path: path, NewFile: true, Diff: "1 + package main\n"}, "Create this file?", []string{"cmd/kn/status.go", "new file", "package main"}},
		{"modified", "edit", &tools.EditPreview{Path: path, Diff: "3 - a := 1\n3 + a := 2\n"}, "Apply this edit?", []string{"+1 −1", "a := 2"}},
		{"too large", "write", &tools.EditPreview{Path: path, Truncated: true}, "Write this file?", []string{"preview truncated", "too large to preview"}},
		{"no change", "edit", &tools.EditPreview{Path: path}, "Apply this edit?", []string{"no lines change"}},
	} {
		reply := openPerm(m, tools.GateRequest{Tool: tc.tool, Command: path, Preview: tc.preview})
		v := ansi.Strip(m.permView())
		for _, w := range append(tc.want, tc.title, "this file · folder · project", "kept for this project") {
			if !strings.Contains(v, w) {
				t.Errorf("%s: card missing %q:\n%s", tc.name, w, v)
			}
		}
		m.permKey(keyRunes("a"))
		<-reply
	}
}

func TestPermPreviewExpandsAndScrolls(t *testing.T) {
	m := compactCmdModel()
	m.width, m.height = 80, 30
	m.permissionMode = "normal"
	var diff strings.Builder
	for i := 1; i <= 40; i++ {
		fmt.Fprintf(&diff, "%d + added line %d\n", i, i)
	}
	reply := openPerm(m, tools.GateRequest{Tool: "write", Command: filepath.Join(cwd(), "big.go"), Preview: &tools.EditPreview{Path: "big.go", NewFile: true, Diff: diff.String()}})
	v := ansi.Strip(m.permView())
	if !strings.Contains(v, "ctrl+e expand") || strings.Contains(v, "added line 40") {
		t.Fatalf("a long preview starts folded:\n%s", v)
	}
	m.permKey(ck(tea.KeyCtrlE))
	v = ansi.Strip(m.permView())
	if !strings.Contains(v, "ctrl+e fold") || !strings.Contains(v, "PgUp/PgDn") {
		t.Fatalf("expanded, the preview scrolls inside the card:\n%s", v)
	}
	if h := strings.Count(m.permView(), "\n") + 1; h > m.cardMaxHeight() {
		t.Fatalf("card is %d rows, more than its %d-row budget", h, m.cardMaxHeight())
	}
	for range 5 {
		m.permKey(ck(tea.KeyPgDown))
		m.permView()
	}
	if v := ansi.Strip(m.permView()); !strings.Contains(v, "added line 40") || !strings.Contains(v, "Yes, once") {
		t.Fatalf("PgDn reaches the end and the options stay in view:\n%s", v)
	}
	m.permKey(keyRunes("e"))
	if m.permDialog.expanded {
		t.Fatal("e folds the preview again")
	}
	m.permKey(keyRunes("1"))
	if ans := <-reply; ans.decision != tools.GateAllowOnce {
		t.Fatalf("1 allows once, got %v", ans.decision)
	}
}

func TestPermLongCommandShownWholeWithinTheScreen(t *testing.T) {
	m := compactCmdModel()
	m.width, m.height = 60, 24
	m.permissionMode = "normal"
	cmd := strings.Repeat("echo step && ", 30) + "echo the-very-end"
	openPerm(m, tools.GateRequest{Tool: "bash", Command: cmd, Shell: "bash"})
	if h := strings.Count(m.permView(), "\n") + 1; h > m.cardMaxHeight() {
		t.Fatalf("card is %d rows, more than its %d-row budget", h, m.cardMaxHeight())
	}
	for range 20 {
		m.permKey(ck(tea.KeyPgDown))
		m.permView()
	}
	if v := ansi.Strip(m.permView()); !strings.Contains(v, "the-very-end") {
		t.Fatalf("scrolling reaches the end of the command:\n%s", v)
	}
}

func TestPermWhyNamesTheReason(t *testing.T) {
	m := compactCmdModel()
	m.width = 100
	m.permissionMode = "normal"
	openPerm(m, tools.GateRequest{Tool: "bash", Command: "make", Shell: "bash", OtherDir: true})
	if v := ansi.Strip(m.permView()); !strings.Contains(v, "Normal mode confirms") || !strings.Contains(v, "runs outside the working directory") {
		t.Fatalf("the card says why it asks:\n%s", v)
	}
}

func multiAsk() (*model, chan askAnswer) {
	m := compactCmdModel()
	m.width = 80
	reply := make(chan askAnswer, 1)
	m.openAsk(askRequest{req: tools.AskRequest{Questions: []tools.AskQuestion{
		{ID: "format", Header: "Format", Prompt: "Which format?", Options: []tools.AskOption{{Label: "Lines"}, {Label: "Array"}}},
		{ID: "fields", Header: "Fields", Prompt: "Which fields?", Multiple: true, Options: []tools.AskOption{{Label: "name"}, {Label: "status"}, {Label: "duration"}}},
		{ID: "when", Header: "When", Prompt: "When to ship?", Options: []tools.AskOption{{Label: "Now"}, {Label: "Next week"}}},
	}}, reply: reply})
	return m, reply
}

func TestMultiQuestionTabsAndOtherAnswers(t *testing.T) {
	m, reply := multiAsk()
	view := func() string { return ansi.Strip(m.askView()) }
	if v := view(); !strings.Contains(v, "Questions 1/3") || !strings.Contains(v, "Format") || !strings.Contains(v, "Submit") {
		t.Fatalf("tab bar missing:\n%s", v)
	}
	m.askKey(keyRunes("1")) // Lines, then on to Fields
	if v := view(); !strings.Contains(v, "Which fields?") || !strings.Contains(v, "✓ Format") {
		t.Fatalf("a single-select answer moves to the next question:\n%s", v)
	}
	m.askKey(keyRunes("1"))
	m.askKey(keyRunes("3"))
	m.askKey(ck(tea.KeyEnter)) // name, duration
	if v := view(); !strings.Contains(v, "When to ship?") {
		t.Fatalf("Enter confirms the picks and moves on:\n%s", v)
	}
	m.askKey(keyRunes("3")) // Other
	for _, r := range "after review" {
		m.askKey(keyRunes(string(r)))
	}
	m.askKey(ck(tea.KeyEnter))
	v := view()
	for _, w := range []string{"1. Which format?", "→ Lines", "→ name, duration", "→ after review", "Enter submits"} {
		if !strings.Contains(v, w) {
			t.Fatalf("the Submit tab sums up the answers, missing %q:\n%s", w, v)
		}
	}
	// Change an answer before submitting.
	m.askKey(ck(tea.KeyRight)) // wraps to Format
	if !strings.Contains(view(), "Which format?") {
		t.Fatalf("→ wraps from Submit to the first question:\n%s", view())
	}
	m.askKey(keyRunes("2"))
	m.askKey(ck(tea.KeyEnter))
	got := <-reply
	if want := []string{"Array", "name, duration", "after review"}; !got.ok || !reflect.DeepEqual(got.answers, want) {
		t.Fatalf("answers = %+v, want %q", got, want)
	}
	if m.askDialog != nil {
		t.Fatal("the card closes on submit")
	}
}

func TestMultiQuestionSubmitWaitsForEveryAnswer(t *testing.T) {
	m, reply := multiAsk()
	m.askKey(ck(tea.KeyShiftTab)) // to Submit
	m.askKey(ck(tea.KeyEnter))
	if v := ansi.Strip(m.askView()); !strings.Contains(v, "question 1 is not answered yet") {
		t.Fatalf("submitting early names the open question:\n%s", v)
	}
	select {
	case a := <-reply:
		t.Fatalf("answered early: %+v", a)
	default:
	}
	m.askKey(ck(tea.KeyEsc))
	if a := <-reply; a.ok {
		t.Fatalf("Esc dismisses: %+v", a)
	}
}

func TestQuestionsFormWithOneQuestionAnswersAtOnce(t *testing.T) {
	m := compactCmdModel()
	reply := make(chan askAnswer, 1)
	m.openAsk(askRequest{req: tools.AskRequest{Questions: []tools.AskQuestion{
		{ID: "q1", Prompt: "Pick tags", Multiple: true, Options: []tools.AskOption{{Label: "a"}, {Label: "b"}}},
	}}, reply: reply})
	if v := ansi.Strip(m.askView()); strings.Contains(v, "Submit") {
		t.Fatalf("one question has no tab bar:\n%s", v)
	}
	m.askKey(keyRunes("1"))
	m.askKey(keyRunes("2"))
	m.askKey(ck(tea.KeyEnter))
	if got := <-reply; !got.ok || !reflect.DeepEqual(got.answers, []string{"a, b"}) {
		t.Fatalf("one entry per question, picks joined: %+v", got)
	}
}

func openPlan(m *model) chan planAnswer {
	reply := make(chan planAnswer, 1)
	m.planMsg(planRequest{plan: "# Plan\n1. Add the flag\n2. Test it", reply: reply})
	return reply
}

func TestPlanApproveLeavesPlanModeForThePreviousMode(t *testing.T) {
	m := compactCmdModel()
	m.width, m.height = 80, 30
	m.permissionMode = "auto_edit"
	m.setPermissionMode("plan")
	reply := openPlan(m)
	v := ansi.Strip(m.planView())
	for _, w := range []string{"Plan ready for review", "1 Plan", "3 2. Test it", "switch to Auto edit", "No, request changes"} {
		if !strings.Contains(v, w) {
			t.Fatalf("plan card missing %q:\n%s", w, v)
		}
	}
	m.planKey(ck(tea.KeyEnter))
	got := <-reply
	if !got.ok || !got.verdict.Approved {
		t.Fatalf("Enter on approve: %+v", got)
	}
	if m.permissionMode != "auto_edit" || m.planOpen() {
		t.Fatalf("mode %q open %v: approving returns to the earlier mode and closes the card", m.permissionMode, m.planOpen())
	}
}

func TestPlanRequestChangesWithLineComments(t *testing.T) {
	m := compactCmdModel()
	m.width, m.height = 80, 30
	m.setPermissionMode("plan")
	reply := openPlan(m)
	typeIn := func(s string) {
		for _, r := range s {
			m.planKey(keyRunes(string(r)))
		}
	}
	m.planKey(ck(tea.KeyDown))
	m.planKey(ck(tea.KeyDown)) // line 3: "2. Test it"
	m.planKey(keyRunes("c"))
	typeIn("cover the error path")
	m.planKey(ck(tea.KeyEnter))
	if v := ansi.Strip(m.planView()); !strings.Contains(v, "└ cover the error path") || !strings.Contains(v, "1 line comment is sent") {
		t.Fatalf("the comment shows under its line:\n%s", v)
	}
	m.planKey(keyRunes("2"))
	m.planKey(ck(tea.KeyBackspace))
	typeIn("split it in two")
	m.planKey(ck(tea.KeyEnter))
	got := <-reply
	if !got.ok || got.verdict.Approved {
		t.Fatalf("request changes: %+v", got)
	}
	for _, w := range []string{"split it in two", "line 3 (2. Test it): cover the error path"} {
		if !strings.Contains(got.verdict.Feedback, w) {
			t.Fatalf("feedback %q missing %q", got.verdict.Feedback, w)
		}
	}
	if m.permissionMode != "plan" {
		t.Fatal("asking for changes stays in plan mode")
	}
}

func TestPlanFeedbackNeedsWordsOrComments(t *testing.T) {
	m := compactCmdModel()
	reply := openPlan(m)
	m.planKey(keyRunes("r"))
	m.planKey(ck(tea.KeyEnter))
	select {
	case a := <-reply:
		t.Fatalf("empty feedback sent: %+v", a)
	default:
	}
	m.planKey(ck(tea.KeyEsc)) // back to the choices
	m.planKey(ck(tea.KeyEsc)) // closes the card
	if a := <-reply; a.ok {
		t.Fatalf("Esc closes without a verdict: %+v", a)
	}
}

func TestPlanCloseMatchesItsRequest(t *testing.T) {
	m := compactCmdModel()
	reply := openPlan(m)
	m.planMsg(planClose{reply: make(chan planAnswer)})
	if !m.planOpen() {
		t.Fatal("a stale close must not close the current card")
	}
	m.planMsg(planClose{reply: reply})
	if m.planOpen() {
		t.Fatal("the matching close closes the card")
	}
}

func TestPlanCardScrollsTheCursorIntoView(t *testing.T) {
	m := compactCmdModel()
	m.width, m.height = 60, 24
	reply := make(chan planAnswer, 1)
	var plan strings.Builder
	for i := 1; i <= 60; i++ {
		fmt.Fprintf(&plan, "%d. step %d\n", i, i)
	}
	m.planMsg(planRequest{plan: plan.String(), reply: reply})
	m.planView()
	for range 59 {
		m.planKey(ck(tea.KeyDown))
	}
	v := ansi.Strip(m.planView())
	if !strings.Contains(v, "60. step 60") || !strings.Contains(v, "Yes, approve") {
		t.Fatalf("the cursor line and the choices stay in view:\n%s", v)
	}
	if h := strings.Count(m.planView(), "\n") + 1; h > m.cardMaxHeight() {
		t.Fatalf("card is %d rows, more than its %d-row budget", h, m.cardMaxHeight())
	}
}

func TestModeBadges(t *testing.T) {
	m := compactCmdModel()
	m.width = 100
	for mode, want := range map[string]string{"plan": "read-only", "auto_edit": "edits allowed", "always": "", "normal": ""} {
		m.permissionMode = mode
		got := ansi.Strip(m.modeBadge())
		if (want == "" && got != "") || !strings.Contains(got, want) {
			t.Errorf("%s badge = %q, want %q", mode, got, want)
		}
	}
	m.permissionMode = "plan"
	m.width = 40
	if strings.Contains(ansi.Strip(m.modeChip()), "read-only") {
		t.Error("the second badge is left out on a narrow row")
	}
}

func TestCardsTakeTypedBurstsKeyByKey(t *testing.T) {
	m, reply := multiAsk()
	m.askKey(keyRunes("1"))
	m.askKey(keyRunes("14b")) // toggle name, open the typed answer, type "b"
	m.askKey(keyRunes("io"))
	m.askKey(ck(tea.KeyEnter))
	m.askKey(keyRunes("2"))
	m.askKey(ck(tea.KeyEnter))
	if got := <-reply; !reflect.DeepEqual(got.answers, []string{"Lines", "name, bio", "Next week"}) {
		t.Fatalf("answers = %q", got.answers)
	}

	m = compactCmdModel()
	plan := openPlan(m)
	m.planKey(keyRunes("2fewer steps"))
	m.planKey(ck(tea.KeyEnter))
	if got := <-plan; got.verdict.Feedback != "fewer steps" {
		t.Fatalf("feedback = %q", got.verdict.Feedback)
	}

	m.permissionMode = "normal"
	perm := openPerm(m, tools.GateRequest{Tool: "bash", Command: "make", Shell: "bash"})
	m.permKey(keyRunes("rno"))
	m.permKey(ck(tea.KeyEnter))
	if got := <-perm; got.decision != tools.GateReject || got.redirect != "no" {
		t.Fatalf("reject = %+v", got)
	}
}

func TestBusyRowWaitsForThePlanReview(t *testing.T) {
	m := compactCmdModel()
	m.width = 100
	m.busy = true
	openPlan(m)
	if row := ansi.Strip(m.busyRow()); !strings.Contains(row, "Waiting for your plan review") || strings.Contains(row, "Esc to interrupt") {
		t.Fatalf("busy row = %q", row)
	}
}
