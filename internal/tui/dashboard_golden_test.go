package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

var dashGoldenNow = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// dashGoldenEntries are fixed sessions, so the goldens do not depend on the
// store's ids or the clock: this workspace (the current session, a recent
// one and an untitled one) and three other workspaces, one of them with a
// turn running in another process.
func dashGoldenEntries(home string) []dashEntry {
	ago := func(d time.Duration) time.Time { return dashGoldenNow.Add(-d) }
	return []dashEntry{
		{meta: session.Meta{ID: "0a0b0c0d", Title: "Gateway rate limiter 限流设计", Model: "glm-5.2-fast", Provider: "inference", CWD: home + "/src/api-gateway", UpdatedAt: ago(time.Minute), UsageIn: 52000, UsageOut: 3100},
			msgs: 24, run: &session.ExternalRun{Client: "kn", PID: 4242, StartedAt: ago(90 * time.Second)}},
		{meta: session.Meta{ID: "c0ffee01", Title: "Refactor the session store", Model: "kimi-k3-fast", Provider: "inference", CWD: home, UpdatedAt: ago(2 * time.Minute), UsageIn: 182000, UsageOut: 9400, Goal: "ship the multi-session dashboard"}, msgs: 41},
		{meta: session.Meta{ID: "a1b2c3d4", Title: "Fix flaky terminal capture test", Model: "glm-5.2-fast", Provider: "inference", CWD: home, UpdatedAt: ago(3 * time.Hour), UsageIn: 40000, UsageOut: 2000}, msgs: 12},
		{meta: session.Meta{ID: "11223344", Title: "Release notes for v0.9", Model: "kimi-k3-fast", Provider: "inference", CWD: "/srv/build/runner", UpdatedAt: ago(26 * time.Hour)}, msgs: 6},
		{meta: session.Meta{ID: "e5f6a7b8", Model: "kimi-k3-fast", Provider: "inference", CWD: home, UpdatedAt: ago(50 * time.Hour)}, msgs: 2},
		{meta: session.Meta{ID: "55667788", Title: "Read the mermaid docs and write a long summary that will not fit on a narrow screen", Model: "kimi-k3-fast", Provider: "inference", CWD: home + "/notes", UpdatedAt: ago(9 * 24 * time.Hour), Tags: []string{"docs"}}, msgs: 9},
	}
}

func dashGoldenTasks() []agent.BackgroundTask {
	return []agent.BackgroundTask{
		{ID: "sub-1", Description: "scan the store for stale run leases", Status: agent.TaskRunning, StartedAt: dashGoldenNow.Add(-40 * time.Second)},
		{ID: "sub-2", Description: "summarize test failures", Status: agent.TaskError, StartedAt: dashGoldenNow.Add(-5 * time.Minute), Report: "exit status 1: go test ./internal/... failed"},
	}
}

// The dashboard scenes pin the screen in every colour mode and size the
// other goldens use: empty, many sessions with subagents, a filter being
// typed, and grouped by workspace.
func TestDashboardGoldens(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Chdir(home)
	st, err := session.Open(filepath.Join(home, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	defer SetLightTheme(false)
	defer setASCIIGlyphs(false)

	scenes := []struct {
		name  string
		empty bool
		keys  []string
	}{
		{name: "dashboard-empty", empty: true},
		{name: "dashboard-many", keys: []string{"j", "j"}},
		{name: "dashboard-filter", keys: []string{"/", "g", "a", "t", "e"}},
		{name: "dashboard-grouped", keys: []string{"tab", "j", "j", "j", "j", "j"}},
	}
	for _, sc := range scenes {
		for _, mode := range goldenModes {
			for _, size := range goldenSizes {
				name := fmt.Sprintf("%s_%s_%dx%d", sc.name, mode.name, size[0], size[1])
				t.Run(name, func(t *testing.T) {
					restore := useProfile(t, mode.profile, mode.dark)
					defer restore()
					SetLightTheme(!mode.dark)
					if mode.mono {
						setMonoMode(true, true)
					}
					setASCIIGlyphs(mode.ascii)
					m := compactCmdModel()
					m.store = st
					m.now = func() time.Time { return dashGoldenNow }
					m.applyAppearance()
					m.Update(mkWinSize(size[0], size[1]))
					m.command("/dashboard")
					if !sc.empty {
						m.sessionID, m.busy = "c0ffee01", true
						m.agent.Todos = []agent.Todo{
							{ID: "t1", Content: "list sessions", Status: "completed"},
							{ID: "t2", Content: "draw the status column", Status: "in_progress"},
							{ID: "t3", Content: "write the docs", Status: "pending"},
						}
						d := m.app.dash
						d.entries, d.tasks = dashGoldenEntries(home), dashGoldenTasks()
						d.total = len(d.entries)
						m.dashRebuild()
					}
					for _, k := range sc.keys {
						if k == "tab" {
							m.dashKey(tea.KeyMsg{Type: tea.KeyTab})
							continue
						}
						m = keys(t, m, k)
					}
					view := m.View()
					lines := strings.Split(view, "\n")
					if len(lines) != size[1] {
						t.Errorf("%d rows on a %d-row screen", len(lines), size[1])
					}
					for i, l := range lines {
						if w := lipgloss.Width(l); w > size[0] {
							t.Errorf("row %d is %d cells wide on a %d-column screen: %q", i, w, size[0], ansi.Strip(l))
						}
					}
					if mode.ascii {
						for _, r := range ansi.Strip(view) {
							if r > 0x7e && (r < 0x4e00 || r > 0x9fff) {
								t.Errorf("ascii view contains %q", r)
								break
							}
						}
					}
					if mode.name == "truecolor-dark" {
						checkGolden(t, name+".ansi", view)
					}
					checkGolden(t, name+".txt", stripView(view))
				})
			}
		}
	}
}
