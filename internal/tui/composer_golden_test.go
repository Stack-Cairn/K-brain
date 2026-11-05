package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// The composer goldens pin the surfaces under and above the composer: the
// history search overlay, paste chips, the queue pane and the slash menu, in
// every colour mode and size the main goldens use. Regenerate with:
//
//	go test ./internal/tui -run TestComposerGoldens -update-golden
var composerGoldenScenes = []struct {
	name  string
	build func(t *testing.T, m *model, clock time.Time) *model
}{
	{"composer-history", func(t *testing.T, m *model, _ time.Time) *model {
		m.hist = []string{
			"deploy the staging server and tail the logs",
			"write tests for the parser",
			"fix the deploy script so it stops on the first error",
			"explain the diff",
		}
		m = press(t, m, ctrlKey(tea.KeyCtrlR))
		return typeKeys(t, m, "dep")
	}},
	{"composer-paste", func(t *testing.T, m *model, _ time.Time) *model {
		m = typeKeys(t, m, "review this ")
		m = press(t, m, pasteMsg(strings.Repeat("func f() {}\n", 120)))
		m = typeKeys(t, m, " and ")
		m = press(t, m, pasteMsg(strings.Repeat("y", 20*1024)))
		return m
	}},
	{"composer-queue", func(t *testing.T, m *model, clock time.Time) *model {
		goldenTurnStart(m)
		m.busy = true
		m.turnStart = clock.Add(-7 * time.Second)
		_, m.cancel = context.WithCancel(context.Background())
		m.queue = []string{"then run the linter", "and update the changelog with a line about the new flag"}
		m.queueSel = 1
		return m
	}},
	{"composer-slash", func(t *testing.T, m *model, _ time.Time) *model {
		m.slashRecent().loaded = true
		m.slashRecent().names = []string{"/model"}
		return typeKeys(t, m, "/mo")
	}},
}

func TestComposerGoldens(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "src", "acme", "kn")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	prevNow := toolTimer.now
	defer func() { toolTimer.now = prevNow }()
	defer SetLightTheme(false)

	for _, scene := range composerGoldenScenes {
		for _, mode := range goldenModes {
			for _, size := range goldenSizes {
				name := fmt.Sprintf("%s_%s_%dx%d", scene.name, mode.name, size[0], size[1])
				t.Run(name, func(t *testing.T) {
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
					m.queueSel = -1
					m.agent.WorkingDir = dir
					m.now = func() time.Time { return clock }
					m.permissionMode = "always"
					m.applyAppearance()
					m.Update(mkWinSize(size[0], size[1]))
					m = scene.build(t, m, clock)
					m.layout()
					view := m.View()

					for i, l := range strings.Split(view, "\n") {
						if w := lipgloss.Width(l); w > size[0] {
							t.Errorf("row %d is %d cells wide on a %d-column screen: %q", i, w, size[0], ansi.Strip(l))
						}
					}
					if mode.mono && (strings.Contains(view, "\x1b[38;") || strings.Contains(view, "\x1b[48;")) {
						t.Errorf("no-colour view carries a colour escape")
					}
					if mode.ascii {
						for _, r := range ansi.Strip(view) {
							if r > 0x7e {
								t.Errorf("ascii view contains %q", r)
								break
							}
						}
					}
					checkGolden(t, name+".ansi", view)
					checkGolden(t, name+".txt", stripView(view))
				})
			}
		}
	}
}
