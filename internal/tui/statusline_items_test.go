package tui

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
)

func TestStatusLineSettingsDefaults(t *testing.T) {
	o := statusLineSettings(&config.Config{})
	if o.interval != statusLineInterval || o.refresh != statusLineStale || o.maxLines != 1 || o.timeout != statusLineTimeout {
		t.Fatalf("defaults: %+v", o)
	}
	o = statusLineSettings(&config.Config{StatusLine: config.StatusLineConfig{IntervalMs: 3000, RefreshMs: 1000, MaxLines: 9}})
	if o.refresh != 3*time.Second || o.maxLines != statusLineMaxLines {
		t.Fatalf("refresh is at least the interval and lines are capped: %+v", o)
	}
	o = statusLineSettings(&config.Config{StatusLine: config.StatusLineConfig{RefreshMs: 5000, MaxLines: 3}})
	if o.refresh != 5*time.Second || o.maxLines != 3 {
		t.Fatalf("%+v", o)
	}
}

func TestStatusLineMultiLineOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	got, err := runStatusLine(`printf 'one\n\n two \nthree\nfour\n'`, nil, 2*time.Second, 3)
	if err != nil || got != "one\ntwo\nthree" {
		t.Fatalf("got %q, %v", got, err)
	}

	m, clock := stClock(t)
	m.Update(mkWinSize(80, 24))
	m.cfg.StatusLine = config.StatusLineConfig{Command: `printf 'ci: green\ndeploy: 2 pending\nqueue: 0\n'`, MaxLines: 2}
	m.Update(m.statusLineCmd(*clock)())
	m.layout()
	if footer := ansi.Strip(m.footerHints()); !strings.Contains(footer, "ci: green") {
		t.Fatalf("footer: %q", footer)
	}
	extra := m.statusLineExtra()
	if len(extra) != 1 || !strings.Contains(ansi.Strip(extra[0]), "deploy: 2 pending") {
		t.Fatalf("extra rows: %q", extra)
	}
	view := m.View()
	if lines := strings.Split(view, "\n"); len(lines) != 24 || !strings.Contains(ansi.Strip(lines[23]), "deploy: 2 pending") {
		t.Fatalf("the second line is the last screen row of 24 (%d rows):\n%s", len(lines), ansi.Strip(view))
	}
}

func TestStatusLineRefreshInterval(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	m, clock := stClock(t)
	m.cfg.StatusLine = config.StatusLineConfig{Command: "echo up", IntervalMs: 1000, RefreshMs: 3000}
	m.Update(m.statusLineCmd(*clock)())
	*clock = clock.Add(2 * time.Second)
	if m.statusLineCmd(*clock) != nil {
		t.Fatal("reran an unchanged session before the refresh interval")
	}
	*clock = clock.Add(1500 * time.Millisecond)
	if m.statusLineCmd(*clock) == nil {
		t.Fatal("no rerun once the refresh interval passed")
	}
}

func TestStatusLineItems(t *testing.T) {
	m, clock := stClock(t)
	m.Update(mkWinSize(120, 30))
	m.git = gitStatus{Repo: "kn", Branch: "main"}
	def := ansi.Strip(m.footerHints())
	if !strings.Contains(def, "kn@main") {
		t.Fatalf("default footer: %q", def)
	}

	m.agent.ContextLimit = 1000
	m.agent.AddUsage(ai.Usage{PromptTokens: 1200, CompletionTokens: 300})
	m.cfg.StatusLine.Items = []string{"time", "branch", "bogus", "Model", "mode", "context", "tokens", "cwd"}
	got := ansi.Strip(m.footerHints())
	want := clock.Format("15:04") + " · kn@main · " + m.modelName + " · " + m.permissionModeLabel() + " · "
	if !strings.Contains(got, want) || !strings.Contains(got, "1.5k tok") || !strings.Contains(got, "% ctx · 1.5k tok · ") {
		t.Fatalf("items footer %q lacks %q", got, want)
	}
	if strings.Contains(got, "bogus") {
		t.Errorf("unknown names are skipped: %q", got)
	}
	if len(m.statusLineExtra()) != 0 {
		t.Errorf("items alone stay on the footer")
	}
	*clock = clock.Add(time.Hour)
	if got := ansi.Strip(m.footerHints()); !strings.Contains(got, clock.Format("15:04")) {
		t.Errorf("time follows the clock: %q", got)
	}
}

// TestStatusLineGolden pins items plus a two-line script on the main screen.
func TestStatusLineGolden(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "src", "acme", "kn")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	restore := useProfile(t, termenv.TrueColor, true)
	defer restore()
	defer SetLightTheme(false)
	SetLightTheme(false)
	m, clock := stClock(t)
	m.permissionMode = "always"
	m.applyAppearance()
	m.Update(mkWinSize(80, 24))
	goldenTurnStart(m)
	m.git = gitStatus{Repo: "kn", Branch: "feat/json"}
	m.agent.ContextLimit = 131072
	m.agent.AddUsage(ai.Usage{PromptTokens: 48210, CompletionTokens: 2140})
	m.cfg.StatusLine = config.StatusLineConfig{Command: "true", Items: []string{"branch", "time"}, MaxLines: 2}
	if m.statusLineCmd(*clock) == nil {
		t.Fatal("no run")
	}
	m.Update(statusLineMsg{seq: m.status().line.seq, text: "ci: green\ndeploy: 2 pending"})
	m.layout()
	view := m.View()
	for i, l := range strings.Split(view, "\n") {
		if w := lipgloss.Width(l); w > 80 {
			t.Errorf("row %d is %d wide", i, w)
		}
	}
	checkGolden(t, "statusline-items_truecolor-dark_80x24.ansi", view)
	checkGolden(t, "statusline-items_truecolor-dark_80x24.txt", stripView(view))
}
