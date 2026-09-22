package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func taskChromeModel() *model {
	m := tasksModel("http://unused")
	m.cfg = &config.Config{Language: "en"}
	m.modelName, m.provName, m.sessTitle = "model1", "provider", "Subagent UI"
	for i := range 8 {
		m.agent.RestoreTask(agent.BackgroundTask{
			ID: fmt.Sprintf("task-%d", i+1), Description: fmt.Sprintf("Review module %d", i+1),
			Prompt: "Inspect the module and report findings.", Status: agent.TaskDone,
			Report:   strings.Repeat("preview line\n", 8) + "last preview line",
			SubModel: "model2", SubUsage: ai.Usage{PromptTokens: 1200, CompletionTokens: 300},
			StartedAt: time.Unix(100+int64(i), 0), EndedAt: time.Unix(165+int64(i), 0),
		})
	}
	return m
}

func TestTaskChromeDockSelectionAndBounds(t *testing.T) {
	m := taskChromeModel()
	m.tasksFocus, m.taskExpanded = true, true
	for _, width := range []int{1, 12, 32, 60, 100, 140} {
		m.width = width
		for selection := range 8 {
			m.taskSel = selection
			dock := m.tasksDock()
			if lipgloss.Height(dock) > tasksDockHeight {
				t.Fatalf("selection %d exceeds dock height: %s", selection, ansi.Strip(dock))
			}
			for _, line := range strings.Split(dock, "\n") {
				if lipgloss.Width(line) > width {
					t.Fatalf("dock overflow at %d: %q", width, ansi.Strip(line))
				}
			}
			if m.dockLo > selection || m.dockLo+len(m.dockOffsets) <= selection {
				t.Fatalf("selected task %d hidden: lo=%d offsets=%v", selection, m.dockLo, m.dockOffsets)
			}
			if width >= 60 && !strings.Contains(ansi.Strip(dock), fmt.Sprintf("task-%d", 8-selection)) {
				t.Fatalf("selected row is missing: %s", ansi.Strip(dock))
			}
		}
	}
}

func TestTaskChromeDetailFitsAndKeepsFooter(t *testing.T) {
	for _, language := range []string{"en", "zh_cn", "zh_Hant"} {
		for _, size := range [][2]int{{24, 12}, {40, 24}, {80, 30}, {120, 40}} {
			m := taskChromeModel()
			m.cfg.Language = language
			m.Update(mkWinSize(size[0], size[1]))
			m.openTask("task-1")
			m.taskVP.buf.WriteString(strings.Repeat("中文和 long output ", 100))
			m.taskVP.input.SetValue("DRAFT")
			m.refreshTaskVP()
			body := m.viewBody()
			if lipgloss.Height(body) != m.height {
				t.Fatalf("%s %v: body height %d, want %d", language, size, lipgloss.Height(body), m.height)
			}
			for _, line := range strings.Split(body, "\n") {
				if lipgloss.Width(line) > m.width {
					t.Fatalf("%s %v: width overflow: %q", language, size, ansi.Strip(line))
				}
			}
			view := ansi.Strip(m.View())
			if !strings.Contains(strings.Split(view, "\n")[0], "task-1") && m.width >= 40 {
				t.Fatalf("task detail header clipped: %s", view)
			}
			if !strings.Contains(view, "DRAFT") || !strings.HasSuffix(view, m.sessTitle) {
				t.Fatalf("composer or session footer missing: %s", view)
			}
			m.closeTaskView()
		}
	}
}

func TestTaskChromeScrollPreservedDuringStreaming(t *testing.T) {
	m := taskChromeModel()
	m.openTask("task-1")
	defer m.closeTaskView()
	tv := m.taskVP
	tv.buf.WriteString(strings.Repeat("line\n", 120))
	m.refreshTaskVP()
	m.taskViewKey(tea.KeyMsg{Type: tea.KeyCtrlHome})
	before := tv.vp.YOffset
	tv.buf.WriteString("new stream output\n")
	m.refreshTaskVP()
	if tv.vp.YOffset != before || tv.vp.AtBottom() {
		t.Fatal("new output stole the scrolled viewport")
	}
	m.taskViewKey(tea.KeyMsg{Type: tea.KeyCtrlEnd})
	if !tv.vp.AtBottom() || !strings.Contains(tv.vp.View(), "new stream output") {
		t.Fatal("ctrl+end should return to the newest output")
	}
	tv.buf.WriteString("last streamed line\n")
	m.refreshTaskVP()
	if !tv.vp.AtBottom() || !strings.Contains(tv.vp.View(), "last streamed line") {
		t.Fatal("latest output should follow the stream")
	}
}

func TestTaskChromeDockClickMatchesPaintedRow(t *testing.T) {
	m := taskChromeModel()
	m.tasksFocus = true
	m.layout()
	lines := strings.Split(ansi.Strip(m.View()), "\n")
	top := m.dockTop()
	if top < 0 || top >= len(lines) || !strings.Contains(lines[top], "task-8") {
		t.Fatalf("dock hit origin %d does not match painted task: %s", top, strings.Join(lines, "\n"))
	}
	m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 5, Y: top + 1})
	if m.taskSel != 1 || !m.taskExpanded {
		t.Fatalf("painted second row selected %d, expanded=%v", m.taskSel, m.taskExpanded)
	}
}

func TestTaskChromeElapsed(t *testing.T) {
	start := time.Unix(100, 0)
	for _, tc := range []struct {
		seconds int
		want    string
	}{{0, "0s"}, {59, "59s"}, {65, "1m05s"}, {3660, "1h01m"}} {
		task := agent.BackgroundTask{StartedAt: start, EndedAt: start.Add(time.Duration(tc.seconds) * time.Second), Status: agent.TaskDone}
		if got := taskElapsed(task, start.Add(24*time.Hour)); got != tc.want {
			t.Errorf("settled elapsed = %s, want %s", got, tc.want)
		}
	}
}

func TestTaskChromeLocalizedSummaryAndHints(t *testing.T) {
	m := taskChromeModel()
	m.tasksFocus = true
	for _, language := range []string{"en", "zh_cn", "zh_Hant"} {
		m.cfg.Language = language
		dock := ansi.Strip(m.tasksDock())
		for _, want := range []string{m.tr("subagents"), "1/8", "8 " + m.tr("done")} {
			if !strings.Contains(dock, want) {
				t.Fatalf("%s missing %q: %s", language, want, dock)
			}
		}
		if strings.Contains(ansi.Strip(m.footerHints()), "shift+tab") {
			t.Fatal("task focus must show task shortcuts, not mode shortcuts")
		}
	}
}

func TestTaskChromeReadOnlyComposerDoesNotWrap(t *testing.T) {
	m := taskChromeModel()
	m.width, m.height = 24, 12
	m.cfg.Language = "zh_Hant"
	m.agent.RestoreTask(agent.BackgroundTask{ID: "history", Status: agent.TaskDone, Restored: true})
	m.openTask("history")
	defer m.closeTaskView()
	if lipgloss.Height(m.viewBody()) != m.height {
		t.Fatalf("read-only label pushed the footer off screen: %s", ansi.Strip(m.viewBody()))
	}
}
