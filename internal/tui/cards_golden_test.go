package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// The card goldens pin each interactive card in every golden mode and size:
// short and long commands, the three edit preview states, the single and
// multi-question ask, and the plan review. Regenerate with:
//
//	go test ./internal/tui -run TestCardGoldens -update-golden

func goldenPerm(m *model, req tools.GateRequest) {
	m.permissionMode = "normal"
	m.receivePermission(permRequest{req: req, reply: make(chan permAnswer, 1)})
}

// goldenModifiedDiff is a 23-line change in the edit tool's diff format: more
// than the folded preview shows.
func goldenModifiedDiff() string {
	var b strings.Builder
	for i := 1; i <= 3; i++ {
		fmt.Fprintf(&b, "%d   \tline %d stays\n", 47+i, i)
	}
	for i := 1; i <= 10; i++ {
		fmt.Fprintf(&b, "%d - \tverbose := flags.Bool(\"v%d\", false, \"verbose\")\n", 50+i, i)
		fmt.Fprintf(&b, "%d + \tasJSON%d := flags.Bool(\"json%d\", false, \"print JSON\")\n", 50+i, i, i)
	}
	return b.String()
}

var cardScenes = []struct {
	name  string
	build func(m *model)
}{
	{"perm-bash-short", func(m *model) {
		goldenPerm(m, tools.GateRequest{Tool: "bash", Command: "go test ./internal/...", Shell: "bash", Rule: tools.CommandRule("go test ./internal/...")})
	}},
	{"perm-bash-long", func(m *model) {
		cmd := "set -euo pipefail\nfor pkg in $(go list ./internal/... | grep -v /vendor/); do\n  echo \"== $pkg\"\n  go test -count=1 -race -timeout 120s \"$pkg\" 2>&1 | tee -a /tmp/kn-test.log\ndone\n" +
			strings.Repeat("go vet ./... && gofmt -l internal cmd\n", 14) + "echo done"
		goldenPerm(m, tools.GateRequest{Tool: "bash", Command: cmd, Shell: "bash", Rule: tools.CommandRule(cmd), Description: "Run the race tests for every internal package"})
	}},
	{"edit-new", func(m *model) {
		goldenPerm(m, tools.GateRequest{Tool: "write", Command: filepath.Join(cwd(), "cmd/kn/json.go"), Preview: &tools.EditPreview{
			Path: "cmd/kn/json.go", NewFile: true,
			Diff: "1 + package main\n2 + \n3 + import \"encoding/json\"\n4 + \n5 + func statusJSON(s Status) ([]byte, error) {\n6 + \treturn json.Marshal(s)\n7 + }\n",
		}})
	}},
	{"edit-modified", func(m *model) {
		goldenPerm(m, tools.GateRequest{Tool: "edit", Command: filepath.Join(cwd(), "cmd/kn/status.go"), Preview: &tools.EditPreview{
			Path: "cmd/kn/status.go", Diff: goldenModifiedDiff(),
		}})
	}},
	{"edit-truncated", func(m *model) {
		goldenPerm(m, tools.GateRequest{Tool: "write", Command: filepath.Join(cwd(), "testdata/big.json"), Preview: &tools.EditPreview{
			Path: "testdata/big.json", Truncated: true,
		}})
	}},
	{"ask-single", func(m *model) {
		m.Update(askRequest{req: tools.AskRequest{
			Question: "Which output format should --json use?",
			Options: []tools.AskOption{
				{Label: "One object per line", Description: "streams well", Recommended: true},
				{Label: "A single array"},
			},
		}, reply: make(chan askAnswer, 1)})
	}},
	{"ask-multi", func(m *model) {
		m.Update(askRequest{req: tools.AskRequest{Questions: []tools.AskQuestion{
			{ID: "format", Header: "Format", Prompt: "Which output format should --json use?", Options: []tools.AskOption{{Label: "Lines", Description: "one object per line"}, {Label: "Array"}}},
			{ID: "fields", Header: "Fields", Prompt: "Which fields go in the output?", Multiple: true, Options: []tools.AskOption{{Label: "name"}, {Label: "status"}, {Label: "duration"}}},
			{ID: "docs", Header: "Docs", Prompt: "Update the README too?", Options: []tools.AskOption{{Label: "Yes"}, {Label: "No"}}},
		}}, reply: make(chan askAnswer, 1)})
		// Answer the first question so the tab bar shows each state.
		m.askKey(keyRunes("1"))
	}},
	{"plan", func(m *model) {
		m.setPermissionMode("plan")
		m.Update(planRequest{plan: "# Add --json to kn status\n\n1. Add a `--json` flag next to the existing ones in cmd/kn/status.go.\n2. Encode Status with encoding/json, one object per call.\n3. Cover both outputs in status_test.go.\n4. Run go test ./cmd/kn/ and go vet ./...", reply: make(chan planAnswer, 1)})
		m.planKey(tea.KeyMsg{Type: tea.KeyDown})
		m.planKey(tea.KeyMsg{Type: tea.KeyDown})
		m.planKey(tea.KeyMsg{Type: tea.KeyDown})
		m.planKey(keyRunes("c"))
		for _, r := range "keep the old text output as the default" {
			m.planKey(keyRunes(string(r)))
		}
		m.planKey(tea.KeyMsg{Type: tea.KeyEnter})
	}},
}

func TestCardGoldens(t *testing.T) {
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

	for _, scene := range cardScenes {
		for _, mode := range goldenModes {
			for _, size := range goldenSizes {
				name := fmt.Sprintf("card-%s_%s_%dx%d", scene.name, mode.name, size[0], size[1])
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
					m.appendRaw(blockUser, "add a --json flag to `kn status`")
					scene.build(m)
					m.layout()
					view := m.View()

					rows := strings.Split(view, "\n")
					if len(rows) > size[1] {
						t.Errorf("%d rows on a %d-row screen", len(rows), size[1])
					}
					for i, l := range rows {
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
