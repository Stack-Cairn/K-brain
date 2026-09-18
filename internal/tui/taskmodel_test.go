package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/config"
	tea "github.com/charmbracelet/bubbletea"
)

func taskCfg(url string) *config.Config {
	return &config.Config{
		DefaultModel: "m",
		Providers:    map[string]config.Provider{"p": {BaseURL: url, APIKey: "k"}},
		Models: map[string]config.Model{
			"m":                     {Providers: []string{"p"}},
			config.DefaultTaskModel: {Providers: []string{"p"}, Context: 384000},
		},
	}
}

func TestTaskCommandSpawns(t *testing.T) {
	srv := sseTextServer(t, "done")
	defer srv.Close()
	m := tasksModel(srv.URL)

	m.taskCommand("poke around the repo and report what you find")
	tasks := m.agent.Tasks().List()
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].Description != "poke around the repo and report what you…" {
		t.Fatalf("auto description: %q", tasks[0].Description)
	}
	waitSettled(t, &tasks[0])

	before := len(m.blocks)
	m.taskCommand("")
	if len(m.blocks) != before+1 || !strings.Contains(m.blocks[len(m.blocks)-1].text, "usage:") {
		t.Fatal("bare /task should print usage")
	}

	m.taskCommand("-m nope do a thing")
	if len(m.agent.Tasks().List()) != 1 {
		t.Fatal("an unresolvable -m model must not spawn a task")
	}
}

func taskmodelCfgModel(url string) *model {
	m := tasksModel(url)
	m.cfg = taskCfg(url)
	m.modelName, m.provName = "m", "p"
	return m
}

func TestSubagentModelCommandPersists(t *testing.T) {
	m := taskmodelCfgModel(sseTextServer(t, "").URL)

	m.subagentModelCommand([]string{"m"})
	if m.cfg.TaskModel != "m" || m.cfg.TaskProvider != "" {
		t.Fatalf("state: %q @ %q", m.cfg.TaskModel, m.cfg.TaskProvider)
	}
	if m.agent.TaskDefault.Client == nil || m.agent.TaskDefault.Model != "m" {
		t.Fatalf("the agent's default subagent route should follow the pick: %+v", m.agent.TaskDefault)
	}
	if !strings.Contains(m.blocks[len(m.blocks)-1].text, "subagent model: m @ p") {
		t.Fatalf("expected a confirmation note, got %q", m.blocks[len(m.blocks)-1].text)
	}

	m.subagentModelCommand([]string{"off"})
	if m.cfg.TaskModel != "" || m.agent.TaskDefault.Model != config.DefaultTaskModel {
		t.Fatalf("off should restore the default route: %q", m.cfg.TaskModel)
	}
	if !strings.Contains(m.blocks[len(m.blocks)-1].text, "default ("+config.DefaultTaskModel+")") {
		t.Fatalf("off should note the default, got %q", m.blocks[len(m.blocks)-1].text)
	}
}

func TestSubagentModelCommandRejectsBadPick(t *testing.T) {
	m := taskmodelCfgModel(sseTextServer(t, "").URL)

	m.subagentModelCommand([]string{"nope"})
	if m.cfg.TaskModel != "" {
		t.Fatal("an unknown model must not persist")
	}
	if !strings.Contains(m.blocks[len(m.blocks)-1].text, "unknown model") {
		t.Fatalf("expected an unknown-model note, got %q", m.blocks[len(m.blocks)-1].text)
	}

	m.cfg.Models["nokey"] = config.Model{Providers: []string{"nokey"}}
	m.cfg.Providers["nokey"] = config.Provider{BaseURL: "http://x"}
	m.subagentModelCommand([]string{"nokey"})
	if m.cfg.TaskModel != "" {
		t.Fatal("an unresolvable route must not persist")
	}
	if !strings.Contains(m.blocks[len(m.blocks)-1].text, "task model:") {
		t.Fatalf("expected a resolve error, got %q", m.blocks[len(m.blocks)-1].text)
	}
}

func TestSubagentModelPanel(t *testing.T) {
	m := taskmodelCfgModel(sseTextServer(t, "").URL)
	m.openPalette()
	var pp *ppanel
	for _, it := range m.palette.all {
		if it.title == "Subagent model" {
			pp = it.panel(m)
		}
	}
	if pp == nil {
		t.Fatal("palette should have a Subagent model row")
	}
	if pp.list[0] != "default ("+config.DefaultTaskModel+")" || len(pp.list) != len(m.cfg.Models)+1 {
		t.Fatalf("panel list: %v", pp.list)
	}

	m.palette.stack = []*ppanel{pp}
	for i, name := range pp.list {
		if model, _ := splitRouteKey(name); model == "m" {
			pp.midx = i
		}
	}
	m.panelKey(tea.KeyMsg{Type: tea.KeyEnter}, pp)
	if m.cfg.TaskModel != "m" || m.palette != nil && len(m.palette.stack) != 0 {
		t.Fatalf("enter should apply and pop: taskModel=%q stack=%v", m.cfg.TaskModel, m.palette)
	}

	pp.midx = 0
	m.palette.stack = []*ppanel{pp}
	m.panelKey(tea.KeyMsg{Type: tea.KeyEnter}, pp)
	if m.cfg.TaskModel != "" {
		t.Fatal("the default row should restore the built-in subagent model")
	}
}

func TestTaskViewChat(t *testing.T) {
	srv := sseTextServer(t, "report")
	defer srv.Close()
	m := tasksModel(srv.URL)
	task := m.agent.StartBackground("probe", "p", agent.SubModel{})
	waitSettled(t, task)

	m.openTask(task.ID)
	tv := m.taskVP
	tv.input.SetValue("what about the tests?")
	m.taskViewKey(tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(tv.buf.String(), "what about the tests?") {
		t.Fatal("the sent message should land in the pane transcript")
	}
	if !tv.busy {
		t.Fatal("a follow-up turn should be in flight")
	}
	if tv.input.Value() != "" {
		t.Fatal("the input should clear on send")
	}

	tv.input.SetValue("more")
	m.taskViewKey(tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(tv.buf.String(), "still replying") {
		t.Fatal("sends while busy should be refused in the pane")
	}
	tv.followCancel()
}

func TestTaskViewRestoredReadOnly(t *testing.T) {
	srv := sseTextServer(t, "x")
	defer srv.Close()
	m := tasksModel(srv.URL)
	m.agent.RestoreTask(agent.BackgroundTask{ID: "task-77", Description: "old", Status: agent.TaskDone, Report: "r", Restored: true})

	m.openTask("task-77")
	m.taskViewKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if m.taskVP.input.Value() != "" {
		t.Fatal("restored tasks must not accept chat input")
	}
	if !strings.Contains(m.taskViewView(), "read-only") {
		t.Fatal("the view should say it is read-only")
	}
}

func TestDownArrowFocusesDockBelowInput(t *testing.T) {
	srv := sseTextServer(t, "ok")
	defer srv.Close()
	m := tasksModel(srv.URL)
	task := m.agent.StartBackground("probe", "p", agent.SubModel{})
	defer m.agent.Tasks().Cancel(task.ID)

	m.key(mkKey("down"))
	if !m.tasksFocus || m.taskSel != 0 {
		t.Fatalf("↓ on empty input should focus the dock at its top row, focus=%v sel=%d", m.tasksFocus, m.taskSel)
	}
	m.key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h")})
	if m.tasksFocus {
		t.Fatal("typing should hand focus back to the input")
	}
	if m.input.Value() != "h" {
		t.Fatalf("the typed rune should land in the input, got %q", m.input.Value())
	}
	m.key(mkKey("down"))
	if m.tasksFocus {
		t.Fatal("↓ with a draft in the input must not steal focus")
	}
}

func TestSubagentModelPanelShowsRoutes(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	m := taskmodelCfgModel(sseTextServer(t, "").URL)
	m.cfg.Providers["q"] = config.Provider{BaseURL: "http://q.example", APIKey: "k"}
	if err := config.SaveCatalogs(map[string]config.Catalog{
		"p": {FetchedAt: time.Now(), BaseURL: "http://p.example", Models: []config.ModelInfoLite{{ID: "shared"}}},
		"q": {FetchedAt: time.Now(), BaseURL: "http://q.example", Models: []config.ModelInfoLite{{ID: "shared"}}},
	}); err != nil {
		t.Fatal(err)
	}
	pp := m.routePanel(panelSubagent, "Subagent model", config.DefaultTaskModel, "", "")
	var shared []int
	for i, row := range pp.list {
		if model, _ := splitRouteKey(row); model == "shared" {
			shared = append(shared, i)
		}
	}
	if len(shared) != 2 {
		t.Fatalf("both routes for a shared id should be rows: %v", pp.list)
	}
	view := m.panelView(pp)
	if !strings.Contains(view, "http://q.example") || !strings.Contains(view, "shared  (new)") {
		t.Fatalf("panel should render provider endpoints like /model:\n%s", view)
	}
	m.palette = &palette{stack: []*ppanel{pp}}
	pp.midx = shared[1]
	m.panelKey(tea.KeyMsg{Type: tea.KeyEnter}, pp)
	if m.cfg.TaskModel != "shared" || m.cfg.TaskProvider != "q" {
		t.Fatalf("pick should persist model and provider, got %q@%q", m.cfg.TaskModel, m.cfg.TaskProvider)
	}
}

func TestSubagentModelPanelWindowsToHeight(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	m := taskmodelCfgModel(sseTextServer(t, "").URL)
	var many []config.ModelInfoLite
	for i := range 300 {
		many = append(many, config.ModelInfoLite{ID: fmt.Sprintf("vendor/model-%03d", i)})
	}
	if err := config.SaveCatalogs(map[string]config.Catalog{"p": {FetchedAt: time.Now(), Models: many}}); err != nil {
		t.Fatal(err)
	}
	m.height = 30
	pp := m.routePanel(panelSubagent, "Subagent model", config.DefaultTaskModel, "", "")
	view := m.panelView(pp)
	lines := strings.Split(view, "\n")
	if len(lines) > 30 {
		t.Fatalf("panel should fit the terminal, got %d lines", len(lines))
	}
	if !strings.Contains(lines[0], "/") || !strings.Contains(view, "type to filter") || !strings.Contains(view, "more") {
		t.Fatalf("query line, footer and overflow marker should render:\n%s", view)
	}
	if !strings.Contains(view, "default (") {
		t.Fatal("selection (row 0) should be in the window")
	}
	pp.midx = 250
	if view = m.panelView(pp); !strings.Contains(view, "vendor/model-249") {
		t.Fatalf("selected row should be visible:\n%s", view)
	}
}

func TestPaletteModelPanelWindowsToHeight(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	m := taskmodelCfgModel(sseTextServer(t, "").URL)
	var many []config.ModelInfoLite
	for i := range 300 {
		many = append(many, config.ModelInfoLite{ID: fmt.Sprintf("vendor/model-%03d", i)})
	}
	if err := config.SaveCatalogs(map[string]config.Catalog{"p": {FetchedAt: time.Now(), Models: many}}); err != nil {
		t.Fatal(err)
	}
	m.height = 30
	pp := &ppanel{kind: panelModel, title: "Model", items: buildModelItems(m.cfg)}
	if n := len(strings.Split(m.panelView(pp), "\n")); n > 30 {
		t.Fatalf("Model panel should fit the terminal, got %d lines", n)
	}
	pp.idx = 200
	if view := m.panelView(pp); !strings.Contains(view, "vendor/model-199") || !strings.Contains(view, "↑ ") || !strings.Contains(view, "↓ ") {
		t.Fatalf("selected route and overflow markers should be visible:\n%s", view)
	}
}
