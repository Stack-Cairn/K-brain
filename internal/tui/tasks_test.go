package tui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func sseTextServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := json.Marshal(body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, `data: {"choices":[{"delta":{"content":%s},"finish_reason":"stop"}]}`+"\n\n", b)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
}

func tasksModel(url string) *model {
	m := &model{
		input:    newInput(),
		agent:    agent.New(ai.New(url, "k"), "m", 100, "sys"),
		queueSel: -1,
	}
	m.width, m.height = 80, 30
	m.input.SetWidth(78)
	return m
}

func tasksModelStore(t *testing.T, url string) *model {
	t.Helper()
	st, err := session.Open(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	m := tasksModel(url)
	m.store = st
	m.cfg = &config.Config{
		DefaultModel: "m",
		Providers:    map[string]config.Provider{"p": {BaseURL: url, APIKey: "k"}},
		Models:       map[string]config.Model{"m": {Providers: []string{"p"}}},
	}
	m.modelName, m.provName = "m", "p"
	return m
}

func TestResumeRestoresTasks(t *testing.T) {
	srv := sseTextServer(t, "ok")
	defer srv.Close()
	m := tasksModelStore(t, srv.URL)

	id, err := m.store.Create("/tmp", "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	msgs := []ai.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "q", Authored: true}, {Role: "assistant", Content: "a"}}
	if err := m.store.Save(id, 1, msgs, "m", "p"); err != nil {
		t.Fatal(err)
	}
	start := time.Now().Add(-time.Hour)
	m.store.SaveTask(id, session.Task{ID: "task-1", Description: "finished probe", Prompt: "p", Status: "done", Report: "the report", StartedAt: start, EndedAt: start.Add(time.Minute)})
	m.store.SaveTask(id, session.Task{ID: "task-2", Description: "died mid-flight", Prompt: "p", Status: "running", StartedAt: start})

	m.agent = agent.New(ai.New(srv.URL, "k"), "m", 100, "sys")
	if err := m.resume(id); err != nil {
		t.Fatal(err)
	}

	tasks := m.agent.Tasks().List()
	if len(tasks) != 2 {
		t.Fatalf("resume should restore 2 tasks, got %d", len(tasks))
	}
	done, ok := m.agent.Tasks().Get("task-1")
	if !ok || done.Status != agent.TaskDone || done.Report != "the report" {
		t.Fatalf("settled task should restore verbatim, got %+v", done)
	}
	stale, ok := m.agent.Tasks().Get("task-2")
	if !ok || stale.Status != agent.TaskError || !strings.Contains(stale.Report, "interrupted") {
		t.Fatalf("a persisted running task must restore as interrupted-error, got %+v", stale)
	}

	dock := stripAll(m.tasksDock())
	if strings.Contains(dock, "finished probe") || strings.Contains(dock, "died mid-flight") {
		t.Fatalf("restored subagents must not clutter the dock, got %q", dock)
	}
	view := stripAll(m.tasksView())
	if !strings.Contains(view, "finished probe") || !strings.Contains(view, "(restored)") {
		t.Fatalf("/tasks should list restored subagents with a marker, got %q", view)
	}

	m.openTask("task-1")
	if m.taskVP.live {
		t.Fatal("a restored settled task must not subscribe to events")
	}
	if !strings.Contains(stripAll(m.taskViewView()), "the report") {
		t.Fatalf("restored task view should show the stored report, got %q", stripAll(m.taskViewView()))
	}
}

func TestResumeHistorySkipsUnauthoredMessages(t *testing.T) {
	srv := sseTextServer(t, "ok")
	defer srv.Close()
	m := tasksModelStore(t, srv.URL)

	id, err := m.store.Create("/tmp", "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	msgs := []ai.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "typed by the human", Authored: true},
		{Role: "assistant", Content: "a"},
		{Role: "user", Content: "[background task task-1 done] PONG", Authored: false},
		{Role: "user", Content: "continue until the goal is met", Authored: false},
		{Role: "user", Content: "another typed one", Authored: true},
	}
	if err := m.store.Save(id, 1, msgs, "m", "p"); err != nil {
		t.Fatal(err)
	}

	m.agent = agent.New(ai.New(srv.URL, "k"), "m", 100, "sys")
	if err := m.resume(id); err != nil {
		t.Fatal(err)
	}
	if len(m.hist) != 2 || m.hist[0] != "typed by the human" || m.hist[1] != "another typed one" {
		t.Fatalf("↑ history should hold only authored messages, got %v", m.hist)
	}
}

func TestTaskPersistsOnStartAndSettle(t *testing.T) {
	srv := sseTextServer(t, "the final report")
	defer srv.Close()
	m := tasksModelStore(t, srv.URL)
	m.wireTasks()

	id, err := m.store.Create("/tmp", "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	m.sessionID = id
	m.agent.Tasks().SetSessionID(id)

	task := m.agent.StartBackground("probe", "p", agent.SubModel{})
	defer m.agent.Tasks().Cancel(task.ID)

	rows, err := m.store.LoadTasks(id)
	if err != nil || len(rows) != 1 || rows[0].Status != "running" {
		t.Fatalf("start should persist a running row: %v %+v", err, rows)
	}

	waitSettled(t, task)
	rows, err = m.store.LoadTasks(id)
	if err != nil || len(rows) != 1 {
		t.Fatalf("settle must not add a row: %v %d", err, len(rows))
	}
	if rows[0].Status != "done" || rows[0].Report != "the final report" {
		t.Fatalf("settle should overwrite with the final state, got %+v", rows[0])
	}
}

func TestTaskPersistsWhenSessionIDAssignedMidFlight(t *testing.T) {

	stream := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		select {
		case <-stream:
		case <-r.Context().Done():
			return
		}
		b, _ := json.Marshal("late report")
		fmt.Fprintf(w, `data: {"choices":[{"delta":{"content":%s},"finish_reason":"stop"}]}`+"\n\n", b)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	m := tasksModelStore(t, srv.URL)
	m.wireTasks()

	task := m.agent.StartBackground("probe", "p", agent.SubModel{})
	defer m.agent.Tasks().Cancel(task.ID)

	id, err := m.store.Create("/tmp", "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	m.agent.Tasks().SetSessionID(id)
	close(stream)

	waitSettled(t, task)
	rows, err := m.store.LoadTasks(id)
	if err != nil || len(rows) != 1 {
		t.Fatalf("the settle should still persist the task: %v %d", err, len(rows))
	}
	if rows[0].Status != "done" || rows[0].Report != "late report" {
		t.Fatalf("got %+v", rows[0])
	}
}

func mkKey(name string) tea.KeyMsg {
	switch name {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "ctrl+t":
		return tea.KeyMsg{Type: tea.KeyCtrlT}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(name)}
}

func waitSettled(t *testing.T, task *agent.BackgroundTask) {
	t.Helper()
	select {
	case <-task.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("task never settled")
	}
}

func TestTasksDockHiddenWithoutTasks(t *testing.T) {
	srv := sseTextServer(t, "ok")
	defer srv.Close()
	m := tasksModel(srv.URL)
	if got := m.tasksDock(); got != "" {
		t.Fatalf("dock should be empty without tasks, got %q", got)
	}
}

func TestTasksDockListsTasks(t *testing.T) {
	srv := sseTextServer(t, "ok")
	defer srv.Close()
	m := tasksModel(srv.URL)
	task := m.agent.StartBackground("probe grafana", "look around", agent.SubModel{})
	defer m.agent.Tasks().Cancel(task.ID)

	dock := stripAll(m.tasksDock())
	if !strings.Contains(dock, task.ID) || !strings.Contains(dock, "probe grafana") {
		t.Fatalf("dock should list the running task, got %q", dock)
	}
	if !strings.Contains(dock, "⏳") {
		t.Fatalf("running task should show the spinner icon, got %q", dock)
	}
}

func TestCtrlTFocusesDockAndArrowsSelect(t *testing.T) {
	srv := sseTextServer(t, "ok")
	defer srv.Close()
	m := tasksModel(srv.URL)
	t1 := m.agent.StartBackground("first", "p", agent.SubModel{})
	defer m.agent.Tasks().Cancel(t1.ID)
	t2 := m.agent.StartBackground("second", "p", agent.SubModel{})
	defer m.agent.Tasks().Cancel(t2.ID)

	m.key(mkKey("ctrl+t"))
	if !m.tasksFocus {
		t.Fatal("ctrl+t should focus the dock")
	}
	if m.taskSel != 0 {
		t.Fatalf("selection should start on the newest task, got %d", m.taskSel)
	}
	m.key(mkKey("down"))
	if m.taskSel != 1 {
		t.Fatalf("↓ should move the selection down, got %d", m.taskSel)
	}
	m.key(mkKey("up"))
	if m.taskSel != 0 {
		t.Fatalf("↑ should move the selection back up, got %d", m.taskSel)
	}
	m.key(mkKey("esc"))
	if !m.tasksFocus {
		t.Fatal("esc must not consume dock focus")
	}
	m.key(mkKey("up"))
	if m.tasksFocus {
		t.Fatal("↑ past the top row should return focus to the input")
	}
}

func TestEnterOpensTaskViewAndEscBacksOut(t *testing.T) {
	srv := sseTextServer(t, "report-body")
	defer srv.Close()
	m := tasksModel(srv.URL)
	task := m.agent.StartBackground("probe", "find things", agent.SubModel{})
	defer m.agent.Tasks().Cancel(task.ID)

	m.key(mkKey("ctrl+t"))
	m.key(mkKey("enter"))
	if m.taskVP == nil || m.taskVP.id != task.ID {
		t.Fatalf("enter should open the selected task, got %+v", m.taskVP)
	}
	body := stripAll(m.taskViewView())
	if !strings.Contains(body, "probe") || !strings.Contains(body, "find things") {
		t.Fatalf("task view should show description and prompt, got %q", body)
	}
	if !strings.Contains(m.View(), "esc back") {
		t.Fatal("the open task view should render the back hint")
	}
	m.key(mkKey("esc"))
	if m.taskVP != nil {
		t.Fatal("esc should close the task view")
	}
	if !m.tasksFocus {
		t.Fatal("esc from a task view should land on the focused dock")
	}
	m.key(mkKey("up"))
	if m.tasksFocus {
		t.Fatal("↑ past the top row should return to the main thread")
	}
}

func TestEnterOnEmptyFocusedDockDoesNotPanic(t *testing.T) {
	srv := sseTextServer(t, "ok")
	defer srv.Close()
	m := tasksModel(srv.URL)

	m.tasksFocus = true
	m.key(mkKey("enter"))
	if m.taskVP != nil {
		t.Fatal("enter on an empty dock should open nothing")
	}

	task := m.agent.StartBackground("probe", "p", agent.SubModel{})
	defer m.agent.Tasks().Cancel(task.ID)
	m.tasksFocus = true
	m.taskSel = 5
	m.key(mkKey("enter"))
	if m.taskVP == nil || m.taskVP.id != task.ID {
		t.Fatalf("enter should clamp to the only task, got %+v", m.taskVP)
	}
}

func TestDockClickSelectsClickedRow(t *testing.T) {
	srv := sseTextServer(t, "ok")
	defer srv.Close()
	m := tasksModel(srv.URL)
	t1 := m.agent.StartBackground("first", "p", agent.SubModel{})
	defer m.agent.Tasks().Cancel(t1.ID)
	t2 := m.agent.StartBackground("second", "p", agent.SubModel{})
	defer m.agent.Tasks().Cancel(t2.ID)

	click := func(y int) tea.Model {
		tm, _ := m.Update(tea.MouseMsg{
			Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, Y: y,
		})
		return tm
	}

	m.layout()
	m2 := click(m.dockTop()).(*model)
	if !m2.tasksFocus || m2.taskVP != nil {
		t.Fatalf("first click should focus, not open: focus=%v vp=%+v", m2.tasksFocus, m2.taskVP)
	}
	m = m2

	m.layout()
	stripTop := m.height - 2 - m.dockRows
	m2 = click(stripTop + 2).(*model)
	if m2.taskSel != 1 || !m2.taskExpanded {
		t.Fatalf("clicking the second task row should select+expand it: sel=%d expanded=%v", m2.taskSel, m2.taskExpanded)
	}
	m = m2

	tm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = tm.(*model)
	if m.taskVP == nil || m.taskVP.id != t1.ID {
		t.Fatalf("enter should open %s, got %+v", t1.ID, m.taskVP)
	}
	m.taskVP = nil
	m.tasksFocus = true

	m2 = click(stripTop).(*model)
	if m2.taskVP != nil {
		t.Fatal("clicking the hint row should not open a task")
	}
	if !m2.tasksFocus {
		t.Fatal("clicking near the dock keeps it focused")
	}
}

func TestDockClickIgnoredWhilePaletteOpen(t *testing.T) {
	srv := sseTextServer(t, "ok")
	defer srv.Close()
	m := tasksModel(srv.URL)
	task := m.agent.StartBackground("probe", "p", agent.SubModel{})
	defer m.agent.Tasks().Cancel(task.ID)

	m.layout()
	top := m.dockTop()
	m.openPalette()
	m2, _ := m.Update(tea.MouseMsg{
		Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, Y: top,
	})
	if m2.(*model).taskVP != nil {
		t.Fatal("a click while the palette is open must not open a dock task")
	}
}

func TestSettledTaskViewShowsReport(t *testing.T) {
	srv := sseTextServer(t, "the final report")
	defer srv.Close()
	m := tasksModel(srv.URL)
	task := m.agent.StartBackground("probe", "p", agent.SubModel{})
	waitSettled(t, task)

	m.openTask(task.ID)
	if m.taskVP.live {
		t.Fatal("a settled task's view should not subscribe to events")
	}
	if !strings.Contains(stripAll(m.taskViewView()), "the final report") {
		t.Fatalf("settled task view should render the report, got %q", stripAll(m.taskViewView()))
	}
	if _, _, ok := m.agent.Tasks().SubscribeWithJournal(task.ID, agent.Events{}); ok {
		t.Fatal("subscribing a settled task should not report live")
	}
}

func TestTaskViewReplaysJournal(t *testing.T) {
	call := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		call++
		switch call {
		case 1:
			fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"t1","type":"function","function":{"name":"read","arguments":"{\"path\":\"/tmp/x\"}"}}]}}]}`+"\n\n")
			fmt.Fprint(w, `data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n")
		default:
			fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"the final report"},"finish_reason":"stop"}]}`+"\n\n")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	m := tasksModel(srv.URL)
	task := m.agent.StartBackground("probe", "p", agent.SubModel{})
	waitSettled(t, task)

	m.openTask(task.ID)
	if m.taskVP.live {
		t.Fatal("a settled task's view should not subscribe to events")
	}
	view := stripAll(m.taskViewView())
	for _, want := range []string{"⚒ read", "the final report", "done:"} {
		if !strings.Contains(view, want) {
			t.Fatalf("replayed view missing %q:\n%s", want, view)
		}
	}
}

func TestRunningTaskViewReplaysThenStreams(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"early-text"}}]}`+"\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"late-text"},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(func() { close(release) })
	defer srv.Close()
	m := tasksModel(srv.URL)
	task := m.agent.StartBackground("probe", "p", agent.SubModel{})
	defer m.agent.Tasks().Cancel(task.ID)

	for range 100 {
		if events, _, _ := m.agent.Tasks().SubscribeWithJournal(task.ID, agent.Events{}); len(events) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	m.openTask(task.ID)
	if !m.taskVP.live {
		t.Fatal("a running task's view should subscribe to live events")
	}
	if view := stripAll(m.taskViewView()); !strings.Contains(view, "early-text") {
		t.Fatalf("view opened mid-run should replay pre-open output:\n%s", view)
	}

	m.Update(taskEventMsg{id: task.ID, kind: 0, s: "late-text"})
	if view := stripAll(m.taskViewView()); !strings.Contains(view, "late-text") {
		t.Fatalf("live event after open missing:\n%s", view)
	}
}

func TestSlashTasksFocusesDockAndOpensByID(t *testing.T) {
	srv := sseTextServer(t, "ok")
	defer srv.Close()
	m := tasksModel(srv.URL)
	task := m.agent.StartBackground("probe", "p", agent.SubModel{})
	defer m.agent.Tasks().Cancel(task.ID)

	m.command("/tasks")
	if !m.tasksFocus {
		t.Fatal("bare /tasks should focus the dock")
	}
	m.command("/tasks " + task.ID)
	if m.taskVP == nil || m.taskVP.id != task.ID {
		t.Fatalf("/tasks <id> should open that task's view, got %+v", m.taskVP)
	}
}

func TestTasksDockShowsSettledTasks(t *testing.T) {
	srv := sseTextServer(t, "ok")
	defer srv.Close()
	m := tasksModel(srv.URL)
	task := m.agent.StartBackground("finished probe", "p", agent.SubModel{})
	waitSettled(t, task)

	dock := stripAll(m.tasksDock())
	if !strings.Contains(dock, "✓") || !strings.Contains(dock, "finished probe") {
		t.Fatalf("dock should show the settled task with a ✓, got %q", dock)
	}
	if !strings.Contains(dock, "done") {
		t.Fatalf("settled row should name its status, got %q", dock)
	}
}

func TestLayoutReservesDockHeight(t *testing.T) {
	srv := sseTextServer(t, "ok")
	defer srv.Close()
	m := tasksModel(srv.URL)
	m.Update(mkWinSize(80, 30))
	base := m.vp.Height

	task := m.agent.StartBackground("probe", "p", agent.SubModel{})
	defer m.agent.Tasks().Cancel(task.ID)
	tm, _ := m.Update(taskUpdateMsg{})
	m = tm.(*model)
	dockRows := lipgloss.Height(m.tasksDock())
	if dockRows != 1 {
		t.Fatalf("one unfocused task should be one dock row, got %d", dockRows)
	}
	if m.vp.Height != base-dockRows {
		t.Fatalf("viewport should shrink by exactly the dock rows: base=%d now=%d dock=%d", base, m.vp.Height, dockRows)
	}

	v := stripAll(m.View())
	di := strings.Index(v, "probe")
	ii := strings.Index(v, "Ask k-brain")
	if di < 0 || ii < 0 || di < ii {
		t.Fatalf("dock must render below the input: dock@%d input@%d\n%s", di, ii, v)
	}
	if m.dockTop() < 0 || m.dockTop() >= m.height {
		t.Fatalf("dockTop out of screen: %d (height %d)", m.dockTop(), m.height)
	}

	m.tasksFocus = true
	tm, _ = m.Update(taskUpdateMsg{})
	m = tm.(*model)
	if m.vp.Height != base-dockRows-1 {
		t.Fatalf("focused dock should cost the hint row too: %d vs %d", m.vp.Height, base-dockRows-1)
	}
}

func TestCtrlTNoopWithoutTasks(t *testing.T) {
	srv := sseTextServer(t, "ok")
	defer srv.Close()
	m := tasksModel(srv.URL)
	m.key(mkKey("ctrl+t"))
	if m.tasksFocus {
		t.Fatal("ctrl+t should not focus an empty dock")
	}
}

func TestDockScrollsWithSelection(t *testing.T) {
	srv := sseTextServer(t, "ok")
	defer srv.Close()
	m := tasksModel(srv.URL)

	for i := range 8 {
		tk := m.agent.StartBackground(fmt.Sprintf("probe-%d", i), "p", agent.SubModel{})
		defer m.agent.Tasks().Cancel(tk.ID)
	}

	m.tasksFocus = true
	m.taskSel = 6
	if got := lipgloss.Height(m.tasksDock()); got > tasksDockHeight {
		t.Fatalf("dock must stay within %d rows, rendered %d", tasksDockHeight, got)
	}
	dock := stripAll(m.tasksDock())
	if !strings.Contains(dock, "probe-1") {
		t.Fatalf("scrolled dock should keep the selection visible, got %q", dock)
	}
	if !strings.Contains(dock, "more") {
		t.Fatalf("dock should advertise hidden rows, got %q", dock)
	}

	rendered := 0
	for line := range strings.Lines(dock) {
		if strings.Contains(line, "probe-") && !strings.Contains(line, "more") {
			rendered++
		}
	}
	if rendered != tasksDockHeight-2 {
		t.Fatalf("scrolled dock should render exactly %d task rows, got %d rows in %q", tasksDockHeight-2, rendered, dock)
	}
}

func TestDockMouseClickExpandsTask(t *testing.T) {
	srv := sseTextServer(t, "ok")
	defer srv.Close()
	m := tasksModel(srv.URL)
	t1 := m.agent.StartBackground("first", "p", agent.SubModel{})
	defer m.agent.Tasks().Cancel(t1.ID)
	t2 := m.agent.StartBackground("second", "p", agent.SubModel{})
	defer m.agent.Tasks().Cancel(t2.ID)

	m.layout()
	top := m.dockTop()
	if n := len(m.dockTasks()); n != 2 {
		t.Fatalf("want 2 dock tasks, got %d", n)
	}

	tm, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 5, Y: top})
	m = tm.(*model)
	if !m.tasksFocus || m.taskSel != 0 {
		t.Fatalf("first click should focus the dock on row 0: focus=%v sel=%d", m.tasksFocus, m.taskSel)
	}
	if m.taskVP != nil {
		t.Fatalf("first click must not open the detail view, got %+v", m.taskVP)
	}

	tm, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 5, Y: m.dockTop() + 1})
	m = tm.(*model)
	if m.taskSel != 1 || !m.taskExpanded {
		t.Fatalf("second click should select+expand row 1: sel=%d expanded=%v", m.taskSel, m.taskExpanded)
	}
	if m.taskVP != nil {
		t.Fatalf("click should expand inline, not open the detail view: %+v", m.taskVP)
	}

	if dock := stripAll(m.tasksDock()); !strings.Contains(dock, "first") {
		t.Fatalf("expanded dock should render the selected task's detail, got %q", dock)
	}

	tm, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 5, Y: m.dockTop() + 1})
	m = tm.(*model)
	if m.taskExpanded {
		t.Fatal("clicking the expanded row should collapse it")
	}

	tm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = tm.(*model)
	if m.taskVP == nil || m.taskVP.id != t1.ID {
		t.Fatalf("enter should open the selected task's detail view, got %+v", m.taskVP)
	}

	m.taskVP = nil
	m.tasksFocus = true
	m.taskExpanded = true
	tm, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown, X: 5, Y: m.dockTop()})
	m = tm.(*model)
	if m.taskExpanded {
		t.Fatal("wheel-scrolling the dock should collapse the expansion")
	}
}

func TestTaskEventAppendsToOpenView(t *testing.T) {
	srv := sseTextServer(t, "ok")
	defer srv.Close()
	m := tasksModel(srv.URL)
	task := m.agent.StartBackground("probe", "p", agent.SubModel{})
	defer m.agent.Tasks().Cancel(task.ID)

	m.openTask(task.ID)
	tm, _ := m.Update(taskEventMsg{id: task.ID, kind: 0, s: "streamed text"})
	m = tm.(*model)
	tm, _ = m.Update(taskEventMsg{id: task.ID, kind: 1, s: "bash", s2: `{"command":"ls"}`})
	m = tm.(*model)
	tm, _ = m.Update(taskEventMsg{id: task.ID, kind: 2, s: "bash", s2: "file1\nfile2"})
	m = tm.(*model)

	buf := m.taskVP.buf.String()
	for _, want := range []string{"streamed text", "⚒ bash", "file1"} {
		if !strings.Contains(stripAll(buf), want) {
			t.Fatalf("open view transcript missing %q: %q", want, stripAll(buf))
		}
	}

	tm, _ = m.Update(taskEventMsg{id: "task-999", kind: 0, s: "stray"})
	m = tm.(*model)
	if strings.Contains(m.taskVP.buf.String(), "stray") {
		t.Fatal("events for other tasks must not leak into the open view")
	}
}

func TestOpenTaskViewRefreshesOnSettle(t *testing.T) {
	srv := sseTextServer(t, "the streamed final report")
	defer srv.Close()
	m := tasksModel(srv.URL)
	task := m.agent.StartBackground("probe", "p", agent.SubModel{})

	m.openTask(task.ID)
	if !m.taskVP.live {
		t.Fatal("view of a running task should be live")
	}
	waitSettled(t, task)
	tm, _ := m.Update(taskUpdateMsg{})
	m = tm.(*model)

	if m.taskVP == nil || m.taskVP.live {
		t.Fatal("settled task's view should no longer be live")
	}
	if !strings.Contains(stripAll(m.taskVP.buf.String()), "the streamed final report") {
		t.Fatalf("refreshed view should show the report, got %q", stripAll(m.taskVP.buf.String()))
	}
	head := stripAll(m.taskViewView())
	if !strings.Contains(head, "(done)") {
		t.Fatalf("header should show the settled status, got %q", head)
	}
}

func TestTaskViewCtrlXCancels(t *testing.T) {
	srv := sseTextServer(t, "ok")
	defer srv.Close()
	m := tasksModel(srv.URL)
	task := m.agent.StartBackground("probe", "p", agent.SubModel{})

	m.openTask(task.ID)
	m.taskViewKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if got := m.taskVP.input.Value(); got != "x" {
		t.Fatalf("plain runes should type into the chat input, got %q", got)
	}
	m.taskViewKey(tea.KeyMsg{Type: tea.KeyCtrlX})
	waitSettled(t, task)
	snap, _ := m.agent.Tasks().Get(task.ID)
	if snap.Status != agent.TaskCancelled {
		t.Fatalf("ctrl+x should cancel the running task, got %s", snap.Status)
	}
}

func TestCtrlTFromTaskViewLandsOnDock(t *testing.T) {
	srv := sseTextServer(t, "ok")
	defer srv.Close()
	m := tasksModel(srv.URL)
	task := m.agent.StartBackground("probe", "p", agent.SubModel{})
	defer m.agent.Tasks().Cancel(task.ID)

	m.openTask(task.ID)
	m.key(mkKey("ctrl+t"))
	if m.taskVP != nil {
		t.Fatal("ctrl+t should close the task view")
	}
	if !m.tasksFocus {
		t.Fatal("ctrl+t from a task view should land on the focused dock")
	}
}

func TestSendTaskMsgNeverBlocksWorker(t *testing.T) {
	sendTaskMsg(nil, taskEventMsg{id: "task-1"})

	p := tea.NewProgram(&model{})
	done := make(chan struct{})
	go func() {
		sendTaskMsg(p, taskEventMsg{id: "task-1", kind: 0, s: "chunk"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("sendTaskMsg blocked on an undrained program — it must detach the Send")
	}
}

func TestRestoredTaskReplaysPersistedTranscript(t *testing.T) {
	srv := sseTextServer(t, "exploration findings here")
	defer srv.Close()
	m := tasksModelStore(t, srv.URL)
	m.wireTasks()

	id, err := m.store.Create("/tmp", "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	m.sessionID = id
	m.agent.Tasks().SetSessionID(id)

	task := m.agent.StartBackground("probe the tree", "find the files", agent.SubModel{})
	waitSettled(t, task)

	if _, err := m.store.SubagentTranscript(id, task.ID); err != nil {
		t.Fatalf("transcript should persist on settle: %v", err)
	}

	m.agent = agent.New(ai.New(srv.URL, "k"), "m", 100, "sys")
	if err := m.resume(id); err != nil {
		t.Fatal(err)
	}
	m.openTask(task.ID)
	if m.taskVP.live {
		t.Fatal("a restored task has no live stream")
	}
	view := stripAll(m.taskViewView())
	for _, want := range []string{"find the files", "exploration findings here"} {
		if !strings.Contains(view, want) {
			t.Fatalf("restored task view should replay the persisted transcript, missing %q:\n%s", want, view)
		}
	}
}

func TestSteeredMessageRendersAsUser(t *testing.T) {
	srv := sseTextServer(t, "ok")
	defer srv.Close()
	m := tasksModel(srv.URL)
	task := m.agent.StartBackground("probe", "p", agent.SubModel{})
	defer m.agent.Tasks().Cancel(task.ID)
	m.openTask(task.ID)

	m.Update(taskEventMsg{id: task.ID, kind: 3, s: "check the other file too"})
	view := stripAll(m.taskViewView())
	if !strings.Contains(view, "you: check the other file too") {
		t.Fatalf("steered message should render as a user turn, got:\n%s", view)
	}
	if strings.Contains(view, "steered:") || strings.Contains(view, "you (steer)") {
		t.Fatalf("steered message must not carry a 'steered' label, got:\n%s", view)
	}
}

func TestSettledTasksLingerUntilUserMessage(t *testing.T) {
	srv := sseTextServer(t, "ok")
	defer srv.Close()
	m := tasksModel(srv.URL)
	task := m.agent.StartBackground("finished probe", "p", agent.SubModel{})
	waitSettled(t, task)

	if len(m.dockTasks()) != 1 {
		t.Fatalf("settled task should stay in the dock, got %d", len(m.dockTasks()))
	}

	m.submitTurn("[subagent done] report", false)
	if len(m.dockTasks()) != 1 {
		t.Fatal("a machine turn must not clear the settled task from the dock")
	}

	m2 := tasksModel(srv.URL)
	task2 := m2.agent.StartBackground("another probe", "p", agent.SubModel{})
	waitSettled(t, task2)
	m2.submitTurn("what's next?", true)
	if len(m2.dockTasks()) != 0 {
		t.Fatalf("a user message should sweep settled tasks, got %d", len(m2.dockTasks()))
	}
}

func TestDockSpaceExpandsTask(t *testing.T) {
	srv := sseTextServer(t, "ok")
	defer srv.Close()
	m := tasksModel(srv.URL)
	task := m.agent.StartBackground("research", "p", agent.SubModel{})
	defer m.agent.Tasks().Cancel(task.ID)
	m.Update(mkWinSize(80, 24))

	tm, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	m = tm.(*model)
	tm, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = tm.(*model)
	if !m.taskExpanded {
		t.Fatal("space on a focused dock should expand the selected task")
	}
	if m.taskVP != nil {
		t.Fatal("space must expand inline, not open the detail view")
	}

	dock := stripAll(m.tasksDock())
	if !strings.Contains(dock, "│ p") {
		t.Fatalf("expanded dock should show the task's prompt, got %q", dock)
	}

	tm, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = tm.(*model)
	if m.taskExpanded {
		t.Fatal("space on the expanded row should collapse it")
	}

	m.tasksFocus = false
	tm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")})
	m = tm.(*model)
	if m.input.Value() != " " {
		t.Fatalf("space without dock focus should type into the input, got %q", m.input.Value())
	}
}

func TestDockTaskExpandContent(t *testing.T) {
	srv := sseTextServer(t, "ok")
	defer srv.Close()
	m := tasksModel(srv.URL)
	task := m.agent.StartBackground("probe", "p", agent.SubModel{})
	defer m.agent.Tasks().Cancel(task.ID)

	if got := m.dockTaskExpand(task.ID); len(got) != 1 || !strings.Contains(stripAll(got[0]), "p") {
		t.Fatalf("fresh task expands to its prompt, got %q", got)
	}
	if got := m.dockTaskExpand("no-such-task"); got != nil {
		t.Fatalf("unknown id expands to nothing, got %q", got)
	}
}

func TestDockClickMapsThroughScrollWindow(t *testing.T) {
	srv := sseTextServer(t, "ok")
	defer srv.Close()
	m := tasksModel(srv.URL)
	for i := range 8 {
		tk := m.agent.StartBackground(fmt.Sprintf("probe-%d", i), "p", agent.SubModel{})
		defer m.agent.Tasks().Cancel(tk.ID)
	}
	m.tasksFocus = true
	m.taskSel = 6
	m.layout()
	if m.dockLo == 0 {
		t.Fatalf("test setup: selection 6 should scroll the window, dockLo=%d", m.dockLo)
	}
	lo := m.dockLo
	top := m.dockTop()

	click := func(y int) {
		tm, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 5, Y: y})
		m = tm.(*model)
	}

	click(top)
	if m.taskSel != lo {
		t.Fatalf("clicking the top visible row should select task %d (window base), got %d", lo, m.taskSel)
	}

	m.layout()
	before := m.taskSel
	dock := stripAll(m.tasksDock())
	if !strings.Contains(dock, "more") {
		t.Fatalf("test setup: dock should show the +N more counter, got %q", dock)
	}
	click(m.dockTop() + m.dockTaskRows)
	if m.taskSel != before {
		t.Fatalf("clicking the +N more row must not change the selection: %d → %d", before, m.taskSel)
	}
}

func TestTaskRowCarriesSubUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"report"},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[],"usage":{"prompt_tokens":123,"completion_tokens":45}}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	m := tasksModelStore(t, srv.URL)
	m.wireTasks()
	id, err := m.store.Create("/tmp", "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	m.sessionID = id
	m.agent.Tasks().SetSessionID(id)

	task := m.agent.StartBackground("probe", "p", agent.SubModel{})
	defer m.agent.Tasks().Cancel(task.ID)
	waitSettled(t, task)

	meta, _, err := m.store.Load("task-" + id + "-" + task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.UsageIn != 123 || meta.UsageOut != 45 {
		t.Fatalf("task row should carry the sub's own bill, got in=%d out=%d", meta.UsageIn, meta.UsageOut)
	}
	if u := m.agent.Usage(); u.PromptTokens != 0 {
		t.Fatalf("parent's own usage must not include the sub: %+v", u)
	}
	if u := m.agent.SubUsage()["m @ "]; u.PromptTokens != 123 || u.CompletionTokens != 45 {
		t.Fatalf("parent ledger should hold the sub's spend: %+v", m.agent.SubUsage())
	}
}
