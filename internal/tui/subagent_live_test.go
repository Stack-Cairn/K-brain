package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

// scriptedSubagentServer plays a parent that delegates one foreground
// subagent; the child reads a file, then reports.
func scriptedSubagentServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"messages"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		child := len(req.Messages) > 0 && strings.Contains(fmt.Sprint(req.Messages[0].Content), "You are a subagent")
		hasTool := false
		for _, msg := range req.Messages {
			hasTool = hasTool || msg.Role == "tool"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		call := func(id, name, args string) {
			b, _ := json.Marshal(args)
			fmt.Fprintf(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":%q,"type":"function","function":{"name":%q,"arguments":%s}}]}}]}`+"\n\n", id, name, b)
			fmt.Fprint(w, `data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1000,"completion_tokens":200}}`+"\n\n")
		}
		text := func(s string) {
			fmt.Fprintf(w, `data: {"choices":[{"delta":{"content":%q},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":20}}`+"\n\n", s)
		}
		switch {
		case child && !hasTool:
			call("child-1", "read", `{"path":"/nonexistent/k-brain-probe.txt"}`)
		case child:
			text("child report")
		case !hasTool:
			call("call-1", "subagent", `{"description":"Survey the loader","prompt":"look around"}`)
		default:
			text("parent done")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
}

// recorder collects what the agent would send the program, in order.
type recorder struct {
	mu   sync.Mutex
	msgs []tea.Msg
}

func (r *recorder) send(msg tea.Msg) {
	r.mu.Lock()
	r.msgs = append(r.msgs, msg)
	r.mu.Unlock()
}

func (r *recorder) all() []tea.Msg {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]tea.Msg(nil), r.msgs...)
}

func TestForegroundSubagentStreamsIntoRowDockAndView(t *testing.T) {
	srv := scriptedSubagentServer(t)
	defer srv.Close()
	m := tasksModel(srv.URL)
	m.cfg = &config.Config{Language: "en"}
	m.Update(mkWinSize(120, 30))
	rec := &recorder{}
	fg := m.fgReg()
	fg.setSend(rec.send)

	// The turn wiring submitTurnAs installs: the registry observes children and
	// learns each foreground subagent call before it runs.
	ctx := agent.WithRequestObserver(context.Background(), fg)
	m.agent.Messages = []ai.Message{{Role: "system", Content: "sys"}}
	_, err := m.agent.Turn(ctx, "delegate", agent.Events{
		OnToolStart: func(id, n, a string) {
			if n == "subagent" && !subagentBackgroundArg(a) {
				fg.expect(id, toolSubject("subagent", a))
			}
			rec.send(toolStartMsg{id, n, a})
		},
		OnToolEnd: func(id, n, r string) {
			if n == "subagent" {
				fg.forget(id)
			}
			rec.send(toolEndMsg{id, n, r})
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	msgs := rec.all()
	ends := 0
	var childID string
	checkedLive := false
	for _, msg := range msgs {
		c, isChange := msg.(subChangedMsg)
		if isChange {
			// The first change is the start, the second the end.
			childID = c.id
			ends++
		}
		if isChange && ends == 2 && !checkedLive {
			// Replay up to the child's end with the child still shown running.
			checkedLive = true
			saved, _ := fg.get(c.id)
			fg.mu.Lock()
			fg.children[c.id].task.Status, fg.children[c.id].task.EndedAt = agent.TaskRunning, time.Time{}
			fg.mu.Unlock()
			m.refreshSubagentRows()
			var row *block
			for i := range m.blocks {
				if m.blocks[i].kind == blockToolRun && m.blocks[i].toolID == "call-1" {
					row = &m.blocks[i]
				}
			}
			if row == nil {
				t.Fatal("no subagent row")
			}
			for _, want := range []string{"◌ running", "1 tool", "Read(/nonexistent/k-brain-probe.txt)", "1.3k tok", "Ctrl+T view"} {
				if !strings.Contains(row.live, want) {
					t.Fatalf("live row missing %q: %q", want, row.live)
				}
			}
			if n := len(m.dockTasks()); n != 1 || !strings.HasPrefix(m.dockTasks()[0].ID, "foreground-") {
				t.Fatalf("dock should list the foreground child, got %+v", m.dockTasks())
			}
			if stats := m.footerStats(); !strings.Contains(stats, "⚙ 1 sub") {
				t.Fatalf("footer should count the running foreground subagent, got %q", stats)
			}
			m.openTask(c.id)
			if m.taskVP == nil || !m.taskVP.live {
				t.Fatal("the subagent view should follow the running child")
			}
			view := stripAll(m.taskViewView())
			for _, want := range []string{"Survey the loader", "foreground", "⚒ read", "read-only"} {
				if !strings.Contains(view, want) {
					t.Fatalf("subagent view missing %q:\n%s", want, view)
				}
			}
			if m.taskChatable(m.taskVP) {
				t.Fatal("a foreground child cannot be steered from the view")
			}
			fg.mu.Lock()
			fg.children[c.id].task = saved
			fg.mu.Unlock()
		}
		m.Update(msg)
	}
	if !checkedLive || childID == "" {
		t.Fatalf("the child never reported through the observer: %#v", msgs)
	}
	if m.taskVP != nil && m.taskVP.live {
		t.Fatal("the view should settle once the child ends")
	}
	m.closeTaskView()

	var header string
	for i := range m.blocks {
		if m.blocks[i].toolID == "call-1" {
			header = ansi.Strip(m.blocks[i].render(m.width))
		}
	}
	for _, want := range []string{"Subagent  Survey the loader", glyphs.OK, "1 tool", "tok"} {
		if !strings.Contains(header, want) {
			t.Fatalf("finished header missing %q: %q", want, header)
		}
	}
	if n := m.runningSubagents(); n != 0 {
		t.Fatalf("no subagent should still run, got %d", n)
	}
	if got := m.dockTasks(); len(got) != 1 || got[0].Status != agent.TaskDone {
		t.Fatalf("the finished child stays in the dock until the next message, got %+v", got)
	}
	m.fgReg().clearSettled()
	if len(m.dockTasks()) != 0 {
		t.Fatal("clearSettled should drop the finished child")
	}
}

func TestForegroundObserverIgnoresUnknownCalls(t *testing.T) {
	fg := newFgRegistry()
	fg.ChildStart(context.Background(), agent.ChildInfo{ID: "foreground-1", Prompt: "nested"}, nil)
	if _, ok := fg.get("foreground-1"); ok {
		t.Fatal("a child without a registered parent call must be ignored")
	}
	ev := fg.ChildEvents("foreground-1")
	if ev.OnText != nil || ev.OnToolStart != nil {
		t.Fatal("unknown children get no event sinks")
	}
}

// blockingServer holds every request open until release is closed.
func blockingServer(t *testing.T) (*httptest.Server, chan struct{}) {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"bg report"},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	return srv, release
}

func TestBackgroundSpawnRowTracksTaskLive(t *testing.T) {
	srv, release := blockingServer(t)
	defer srv.Close()
	m := tasksModel(srv.URL)
	m.cfg = &config.Config{Language: "en"}
	m.Update(mkWinSize(120, 30))
	task := m.agent.StartBackground("Scan docs for TODOs", "p", agent.SubModel{})

	args := `{"description":"Scan docs for TODOs","prompt":"p","background":true}`
	m.Update(toolStartMsg{"c1", "subagent", args})
	m.Update(toolEndMsg{"c1", "subagent", "Started background subagent " + task.ID + ": Scan docs for TODOs. Keep working on something else; the report will arrive as a message when it finishes. Do not poll for it."})
	last := m.blocks[len(m.blocks)-1]
	if !last.subLine || last.subID != task.ID {
		t.Fatalf("the spawn result should become a live status row, got %+v", last)
	}
	m.Update(subProgressMsg{id: task.ID, tool: "Grep(TODO)"})
	m.Update(subProgressMsg{id: task.ID, usage: ai.Usage{PromptTokens: 900, CompletionTokens: 300}})
	row := stripAll(m.blocks[len(m.blocks)-1].render(m.width))
	for _, want := range []string{"⎿ ◌ " + task.ID + " running in background", "1 tool", "Grep(TODO)", "1.2k tok", "Ctrl+T view"} {
		if !strings.Contains(row, want) {
			t.Fatalf("background row missing %q: %q", want, row)
		}
	}
	if strings.Contains(stripAll(m.viewBody()), "Do not poll") {
		t.Fatal("the model-facing spawn text should not reach the transcript")
	}
	dock := stripAll(m.tasksDock())
	if !strings.Contains(dock, "Grep(TODO)") || !strings.Contains(dock, "running · 1 tool ·") {
		t.Fatalf("dock row should carry the live activity, got %q", dock)
	}

	close(release)
	waitSettled(t, task)
	m.Update(taskUpdateMsg{})
	row = stripAll(m.blocks[len(m.blocks)-1].render(m.width))
	if !strings.Contains(row, "✓ "+task.ID+" done") || strings.Contains(row, "in background") || strings.Contains(row, "Grep(TODO)") {
		t.Fatalf("finished row should summarise, got %q", row)
	}
}

func TestCtrlXStopsSelectedDockTask(t *testing.T) {
	srv, release := blockingServer(t)
	defer srv.Close()
	defer close(release)
	m := tasksModel(srv.URL)
	m.cfg = &config.Config{Language: "en"}
	task := m.agent.StartBackground("long probe", "p", agent.SubModel{})

	m.key(mkKey("ctrl+t"))
	m.key(tea.KeyMsg{Type: tea.KeyCtrlX})
	if !m.subStop[task.ID] {
		t.Fatal("ctrl+x should mark the task as stopping")
	}
	if !strings.Contains(m.transientNotice, "stopping "+task.ID) {
		t.Fatalf("ctrl+x should confirm in the footer, got %q", m.transientNotice)
	}
	waitSettled(t, task)
	got, _ := m.agent.Tasks().Get(task.ID)
	if got.Status != agent.TaskCancelled {
		t.Fatalf("ctrl+x should cancel the task, got %s", got.Status)
	}
	m.Update(taskUpdateMsg{})
	if m.subStop[task.ID] {
		t.Fatal("the stopping mark should clear once the task settles")
	}
	if !m.tasksFocus {
		t.Fatal("stopping keeps the dock focused")
	}
	m.key(tea.KeyMsg{Type: tea.KeyCtrlX})
	if !strings.Contains(m.transientNotice, "already finished") {
		t.Fatalf("stopping a finished task should say so, got %q", m.transientNotice)
	}
}

func TestDockListsRunningFirstAndKeepsSelection(t *testing.T) {
	m := tasksModel("http://unused")
	m.cfg = &config.Config{Language: "en"}
	fg := m.fgReg()
	base := time.Unix(1000, 0)
	add := func(id string, status agent.TaskStatus, at int) {
		fg.children[id] = &fgChild{task: agent.BackgroundTask{ID: id, Description: id, Status: status, StartedAt: base.Add(time.Duration(at) * time.Second), EndedAt: base.Add(time.Minute)}}
	}
	add("old-done-1", agent.TaskDone, 3)
	add("running-2", agent.TaskRunning, 1)
	add("newer-done-3", agent.TaskDone, 2)
	ids := func() []string {
		var out []string
		for _, task := range m.dockList() {
			out = append(out, task.ID)
		}
		return out
	}
	if got := strings.Join(ids(), ","); got != "running-2,old-done-1,newer-done-3" {
		t.Fatalf("running rows first, then newest finished: %s", got)
	}
	m.tasksFocus, m.taskSel = true, 1
	m.tasksDock()
	fg.children["running-2"].task.Status = agent.TaskDone
	if got := strings.Join(ids(), ","); got != "old-done-1,newer-done-3,running-2" {
		t.Fatalf("order after the running task finished: %s", got)
	}
	if m.taskSel != 0 {
		t.Fatalf("the selection should follow old-done-1 to row 0, got %d", m.taskSel)
	}
}

func TestBackgroundNoticesRenderAsStatusRows(t *testing.T) {
	m := tasksModel("http://unused")
	m.cfg = &config.Config{Language: "en"}
	m.Update(mkWinSize(100, 30))
	notice := "[subagent scan-docs-3 done] Scan docs\n\nfound 2 TODOs\nline two\nline three\nline four"
	m.Update(steeredMsg(notice))
	for _, b := range m.blocks {
		if b.kind == blockUser {
			t.Fatal("a notice must not render as user input")
		}
	}
	view := stripAll(m.viewBody())
	if strings.Contains(view, "(steered)") || strings.Contains(view, "❯ [subagent") {
		t.Fatalf("notice rendered like a steer: %s", view)
	}
	for _, want := range []string{glyphs.ToolDone + " subagent scan-docs-3 done · Scan docs", glyphs.Result + " found 2 TODOs", "+1 lines · ctrl+e"} {
		if !strings.Contains(view, want) {
			t.Fatalf("notice view missing %q:\n%s", want, view)
		}
	}

	m.Update(steeredMsg("please also check the tests"))
	if last := m.blocks[len(m.blocks)-1]; !strings.Contains(stripAll(last.text), "(steered)") {
		t.Fatalf("a real steer keeps its echo, got %q", stripAll(last.text))
	}
}

func TestParseNoticeShapes(t *testing.T) {
	for _, tc := range []struct {
		in, kind, id, status, title string
		ok                          bool
	}{
		{"[subagent a-1 done] Survey\n\nbody", "subagent", "a-1", "done", "Survey", true},
		{"[subagent a-1] prepare subagent workspace: boom", "subagent", "a-1", "", "prepare subagent workspace: boom", true},
		{"[workflow w-2 complete] build", "workflow", "w-2", "complete", "build", true},
		{"[wait w3 timeout] deadline elapsed", "wait", "w3", "timeout", "deadline elapsed", true},
		{"[note] hello", "", "", "", "", false},
		{"please [subagent a-1 done]", "", "", "", "", false},
	} {
		n, ok := parseNotice(tc.in)
		if ok != tc.ok || n.kind != tc.kind || n.id != tc.id || n.status != tc.status || n.title != tc.title {
			t.Errorf("parseNotice(%q) = %+v, %v", tc.in, n, ok)
		}
	}
}

func TestOrphanedNoticeRunsAsBackgroundTurn(t *testing.T) {
	m := busyQueueModel()
	m.busy = false
	m.agent.Client = stubLLM()
	m.agent.Messages = []ai.Message{{Role: "system", Content: "sys"}}
	notice := "[subagent probe-1 done] probe\n\nreport"

	tm, _ := m.Update(orphanSteerMsg(notice))
	m = tm.(*model)
	if !m.busy {
		t.Fatal("the notice should start a turn")
	}
	var got ai.Message
	for range 200 {
		for _, msg := range m.agent.MessagesSnapshot() {
			if msg.Role == "user" && msg.Content == notice {
				got = msg
			}
		}
		if got.Content != "" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if got.Content == "" {
		t.Fatal("the notice never reached the model")
	}
	if got.Authored || got.Origin != ai.OriginBackground {
		t.Fatalf("the notice must be a background message, got authored=%v origin=%q", got.Authored, got.Origin)
	}
	for _, b := range m.blocks {
		if b.kind == blockUser {
			t.Fatal("the notice must not render as user input")
		}
	}
}

func TestWaitWakeRunsAsBackgroundTurn(t *testing.T) {
	m := busyQueueModel()
	m.busy = false
	m.agent.Client = stubLLM()
	m.agent.Messages = []ai.Message{{Role: "system", Content: "sys"}}
	wake := "[wait w-1 done] condition met (ran every 5s):\n$ test -f ready\n\nok"

	tm, _ := m.Update(waitWakeMsg(wake))
	m = tm.(*model)
	view := ""
	for _, b := range m.blocks {
		if b.kind == blockUser {
			t.Fatal("a wait wake must not render as user input")
		}
		view += stripAll(b.text) + "\n"
	}
	if strings.Count(view, "w-1") != 1 || !strings.Contains(view, "⏲ wait w-1 done") {
		t.Fatalf("the wake should show once as a notice row, got:\n%s", view)
	}
}

func TestResumeRendersBackgroundNotices(t *testing.T) {
	m := tasksModel("http://unused")
	m.Update(mkWinSize(100, 30))
	m.seedTranscript([]ai.Message{
		{Role: "user", Content: "hi", Authored: true},
		{Role: "user", Content: "[subagent x-1 error] probe\n\nboom", Origin: ai.OriginBackground},
	}, 0)
	kinds := []blockKind{}
	for _, b := range m.blocks {
		kinds = append(kinds, b.kind)
	}
	if len(m.blocks) != 3 || m.blocks[0].kind != blockUser || m.blocks[1].kind == blockUser {
		t.Fatalf("resumed notice should render as a status row, got kinds %v", kinds)
	}
	if got := stripAll(m.blocks[1].text); !strings.HasPrefix(got, "◆ subagent x-1 error") {
		t.Fatalf("resumed notice header = %q", got)
	}
}

// TestSubagentRowsSnapshot pins the exact rendered rows at a fixed clock.
func TestSubagentRowsSnapshot(t *testing.T) {
	m := tasksModel("http://unused")
	m.cfg = &config.Config{Language: "en"}
	now := time.Unix(2000, 0)
	m.now = func() time.Time { return now }
	m.width = 120
	fg := m.fgReg()
	fg.children["foreground-7"] = &fgChild{toolID: "call-1", task: agent.BackgroundTask{ID: "foreground-7", Description: "Survey the loader", Status: agent.TaskRunning, StartedAt: now.Add(-12 * time.Second)}}
	m.applySubProgress(subProgressMsg{id: "foreground-7", tool: "Read(internal/config/load.go)"})
	m.applySubProgress(subProgressMsg{id: "foreground-7", tool: "Grep(LoadConfig)"})
	m.applySubProgress(subProgressMsg{id: "foreground-7", usage: ai.Usage{PromptTokens: 3000, CompletionTokens: 400}})
	task, _ := m.subTask("foreground-7")

	golden := []struct{ name, got, want string }{
		{"running foreground", stripAll(m.subagentRowText(task, false)),
			"  ⎿ ◌ running · 2 tools · Grep(LoadConfig) · 12s · 3.4k tok · Ctrl+T view"},
		{"running background", stripAll(m.subagentRowText(task, true)),
			"  ⎿ ◌ foreground-7 running in background · 2 tools · Grep(LoadConfig) · 12s · 3.4k tok · Ctrl+T view"},
		{"dock row", strings.TrimRight(stripAll(m.taskDockRow(task, false)), " "),
			"   ◌ foreground-7  Survey the loader · Grep(LoadConfig)                          running · 2 tools · 12s · 3.0k/400 tok"},
	}
	fg.children["foreground-7"].task.Status = agent.TaskDone
	fg.children["foreground-7"].task.EndedAt = now.Add(31 * time.Second)
	task, _ = m.subTask("foreground-7")
	golden = append(golden,
		struct{ name, got, want string }{"done background", stripAll(m.subagentRowText(task, true)),
			"  ⎿ ✓ foreground-7 done · 2 tools · 43s · 3.4k tok"},
		struct{ name, got, want string }{"notice", stripAll(m.noticeBlocks("[subagent foreground-7 done] Survey the loader\n\nreport")[0].text),
			"◆ subagent foreground-7 done · Survey the loader · 2 tools · 43s · 3.4k tok"},
	)
	for _, g := range golden {
		if g.got != g.want {
			t.Errorf("%s:\n got %q\nwant %q", g.name, g.got, g.want)
		}
	}
}

func TestResumeKeepsRestoredTaskOutOfLiveCounts(t *testing.T) {
	m := tasksModelStore(t, "http://unused")
	id, err := m.store.Create("/tmp", "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.store.Save(id, 1, []ai.Message{{Role: "system", Content: "sys"}}, "m", "p"); err != nil {
		t.Fatal(err)
	}
	m.store.SaveTask(id, session.Task{ID: "t-1", Description: "old", Prompt: "p", Status: "running", StartedAt: time.Now()})
	if err := m.resume(id); err != nil {
		t.Fatal(err)
	}
	if n := m.runningSubagents(); n != 0 {
		t.Fatalf("a restored task is not running, got %d", n)
	}
}
