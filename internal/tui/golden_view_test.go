package tui

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// The golden views pin the whole screen for each themed surface, in every
// colour mode the theme degrades through. Each case stores the raw ANSI frame
// and its stripped text grid, so a diff shows both what moved and what
// changed colour. Regenerate with:
//
//	go test ./internal/tui -run TestGoldenViews -update-golden
var updateGolden = flag.Bool("update-golden", false, "rewrite the golden view files under testdata/golden")

type goldenMode struct {
	name    string
	profile termenv.Profile
	dark    bool
	mono    bool
	ascii   bool
}

var goldenModes = []goldenMode{
	{name: "truecolor-dark", profile: termenv.TrueColor, dark: true},
	{name: "truecolor-light", profile: termenv.TrueColor, dark: false},
	{name: "256-dark", profile: termenv.ANSI256, dark: true},
	{name: "16-dark", profile: termenv.ANSI, dark: true},
	{name: "nocolor", profile: termenv.Ascii, dark: true, mono: true},
	{name: "ascii", profile: termenv.ANSI, dark: true, ascii: true},
}

var goldenSizes = [][2]int{{80, 24}, {120, 40}, {40, 24}}

// goldenScenes are the mockups: the welcome card, a turn with every tool row
// state and an edit diff, the approval card, and the question card.
var goldenScenes = []struct {
	name  string
	build func(m *model, clock *time.Time)
}{
	{"welcome", func(m *model, _ *time.Time) { m.appendBanner() }},
	{"turn", goldenTurn},
	{"approval", func(m *model, clock *time.Time) {
		m.permissionMode = "normal"
		goldenTurnStart(m)
		m.receivePermission(permRequest{req: tools.GateRequest{
			Tool: "bash", Command: "go build -o bin/kn ./cmd/kn", Shell: "bash",
			Rule: tools.CommandRule("go build -o bin/kn ./cmd/kn"),
		}, reply: make(chan permAnswer, 1)})
	}},
	{"question", func(m *model, _ *time.Time) {
		goldenTurnStart(m)
		m.Update(askRequest{req: tools.AskRequest{
			Question: "Which output format should --json use?",
			Options: []tools.AskOption{
				{Label: "One object per line", Description: "streams well", Recommended: true},
				{Label: "A single array"},
				{Label: "Pretty-printed object"},
			},
		}, reply: make(chan askAnswer, 1)})
	}},
}

func goldenTurnStart(m *model) {
	m.appendRaw(blockUser, "add a --json flag to `kn status` and run the tests")
	m.Update(textMsg("I'll add the flag next to the existing ones.\n\n```go\nfunc statusJSON(w io.Writer, s Status) error {\n\t// one object per call\n\treturn json.NewEncoder(w).Encode(s)\n}\n```\n\nThen run the package tests."))
	m.flushCurrent()
}

func goldenTurn(m *model, clock *time.Time) {
	goldenTurnStart(m)
	tick := func(d time.Duration) { *clock = clock.Add(d) }

	m.Update(toolStartMsg{id: "e1", name: "edit", args: `{"path":"cmd/kn/status.go","old_string":"x","new_string":"y"}`})
	tick(80 * time.Millisecond)
	m.Update(toolEndMsg{id: "e1", name: "edit", result: "Replaced 1 occurrence(s) in cmd/kn/status.go\n```diff\n41   \tflags := pflag.NewFlagSet(\"status\")\n42 - \tverbose := flags.Bool(\"v\", false, \"verbose\")\n42 + \tasJSON := flags.Bool(\"json\", false, \"print JSON\")\n```"})

	m.Update(toolStartMsg{id: "b1", name: "bash", args: `{"command":"go test ./cmd/kn/"}`})
	tick(2400 * time.Millisecond)
	m.Update(toolEndMsg{id: "b1", name: "bash", result: "ok  \tgithub.com/acme/kn/cmd/kn\t2.1s"})

	m.Update(toolStartMsg{id: "b2", name: "bash", args: `{"command":"go vet ./..."}`})
	tick(1100 * time.Millisecond)
	m.Update(toolEndMsg{id: "b2", name: "bash", result: "Error: exit status 1\ncmd/kn/status.go:42:2: declared and not used: asJSON"})

	m.appendRaw(blockText, errStyle.Render("error: provider returned 529: overloaded, retrying in 4s"))
	m.appendRaw(blockText, warnStyle.Render("⚠ context is 82% full — /compact soon"))

	m.Update(toolStartMsg{id: "r1", name: "read", args: `{"path":"cmd/kn/status.go"}`})
	m.Update(toolCallMsg{id: "g1", name: "grep", args: `{"pattern":"asJSON"}`})
	tick(3 * time.Second)

	m.busy = true
	m.turnStart = clock.Add(-12 * time.Second)
}

func TestGoldenViews(t *testing.T) {
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

	for _, scene := range goldenScenes {
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
					m.now = func() time.Time { return clock }
					m.permissionMode = "always"
					m.applyAppearance()
					m.Update(mkWinSize(size[0], size[1]))
					scene.build(m, &clock)
					m.layout()
					view := m.View()

					for i, l := range strings.Split(view, "\n") {
						if w := lipgloss.Width(l); w > size[0] {
							t.Errorf("row %d is %d cells wide on a %d-column screen: %q", i, w, size[0], ansi.Strip(l))
						}
					}
					if mode.mono && strings.Contains(view, "\x1b[38;") || mode.mono && strings.Contains(view, "\x1b[48;") {
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

// stripView is the text grid: escapes gone, trailing blanks trimmed per row.
func stripView(v string) string {
	lines := strings.Split(ansi.Strip(v), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return strings.Join(lines, "\n") + "\n"
}

func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join(goldenDir(t), name)
	if *updateGolden {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden %s (run with -update-golden): %v", name, err)
	}
	if !bytes.Equal(want, []byte(got)) {
		t.Errorf("%s differs from the golden file; rerun with -update-golden if the change is intended.\n--- want\n%s\n--- got\n%s",
			name, ansi.Strip(string(want)), ansi.Strip(got))
	}
}

var goldenRoot string

// goldenDir is testdata/golden next to this file; the test chdirs away, so
// the path is fixed before that.
func goldenDir(t *testing.T) string {
	t.Helper()
	if goldenRoot == "" {
		t.Fatal("golden directory not resolved")
	}
	if err := os.MkdirAll(goldenRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	return goldenRoot
}

func init() {
	if wd, err := os.Getwd(); err == nil {
		goldenRoot = filepath.Join(wd, "testdata", "golden")
	}
}
