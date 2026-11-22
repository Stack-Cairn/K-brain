package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

// useCompact turns compact density on for one test.
func useCompact(t *testing.T) {
	t.Helper()
	prev := compactDensity
	compactDensity = true
	t.Cleanup(func() { compactDensity = prev })
}

func TestDensityCommandSavesAndApplies(t *testing.T) {
	m := surfaceModel(t)
	t.Cleanup(func() { compactDensity = false })
	m.command("/density")
	if v := surfScreen(m); !strings.Contains(v, "density: comfortable") || compactDensity {
		t.Fatalf("bare /density reports and changes nothing:\n%s", v)
	}
	m.command("/density compact")
	if !compactDensity || m.cfg.Density != "compact" {
		t.Fatalf("compact: on=%v saved=%q", compactDensity, m.cfg.Density)
	}
	saved, err := config.Load()
	if err != nil || saved.Density != "compact" {
		t.Fatalf("saved config density %q, %v", saved.Density, err)
	}
	m.command("/density roomy")
	if v := surfScreen(m); !strings.Contains(v, "usage: /density comfortable|compact") {
		t.Errorf("a bad argument shows usage:\n%s", v)
	}
	m.command("/density comfortable")
	if compactDensity || m.cfg.Density != "" {
		t.Errorf("comfortable is the default and is saved as such: on=%v saved=%q", compactDensity, m.cfg.Density)
	}
	var row settingDef
	for _, d := range settingDefs() {
		if d.name == "Density" {
			row = d
		}
	}
	row.step(m, 1)
	if !compactDensity || row.value(m) != "compact" {
		t.Errorf("the settings row should switch to compact")
	}
	row.step(m, 1)
	if compactDensity {
		t.Errorf("and back")
	}
}

// goldenTurnModel builds the representative turn at w×h.
func goldenTurnModel(t *testing.T, w, h int) *model {
	t.Helper()
	clock := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	prevNow := toolTimer.now
	toolTimer.now = func() time.Time { return clock }
	t.Cleanup(func() { toolTimer.now = prevNow })
	m := compactCmdModel()
	m.now = func() time.Time { return clock }
	m.permissionMode = "always"
	m.applyAppearance()
	m.Update(mkWinSize(w, h))
	goldenTurn(m, &clock)
	m.layout()
	return m
}

// Compact folds successful tool output away, keeps a failure's output, and
// leaves a blank row only before a prompt.
func TestCompactTranscript(t *testing.T) {
	useCompact(t)
	m := goldenTurnModel(t, 80, 40)
	text := ansi.Strip(strings.Join(referenceRows(m), "\n"))
	if strings.Contains(text, "github.com/acme/kn/cmd/kn") || strings.Contains(text, "verbose := flags") {
		t.Errorf("successful output should be folded away:\n%s", text)
	}
	if !strings.Contains(text, "declared and not used: asJSON") {
		t.Errorf("a failed call keeps its output:\n%s", text)
	}

	m = compactCmdModel()
	m.Update(mkWinSize(80, 24))
	mixedTranscript(m)
	checkIndex(t, m, "compact")
	prev := -1
	for i := range m.blocks {
		b := &m.blocks[i]
		if b.y0 < 0 {
			continue
		}
		if b.kind == blockTool && b.y0 >= 0 && !b.expanded {
			t.Errorf("block %d: a successful result is drawn", i)
		}
		if prev >= 0 {
			want := 0
			if b.kind == blockUser {
				want = 1
			}
			if gap := b.y0 - m.blocks[prev].y1 - 1; gap != want {
				t.Errorf("block %d (kind %d): %d blank rows before it, want %d", i, b.kind, gap, want)
			}
		}
		prev = i
	}

	// Opening a header shows its output.
	for i := range m.blocks {
		if m.blocks[i].kind == blockToolRun && m.blocks[i].toolID == "t2" {
			m.blocks[i].toggle()
		}
	}
	m.refreshVP()
	checkIndex(t, m, "header opened")
	if n := strings.Count(ansi.Strip(strings.Join(referenceRows(m), "\n")), "line five"); n != 1 {
		t.Errorf("an opened header shows its output once, got %d", n)
	}

	// Switching back restores the comfortable layout exactly.
	for i := range m.blocks {
		if m.blocks[i].kind == blockToolRun && m.blocks[i].toolID == "t2" {
			m.blocks[i].toggle()
		}
	}
	compactDensity = false
	m.restyleTranscript()
	checkIndex(t, m, "comfortable again")
	ref := compactCmdModel()
	ref.Update(mkWinSize(80, 24))
	mixedTranscript(ref)
	if !slices.Equal(referenceRows(m), referenceRows(ref)) {
		t.Error("comfortable after compact should match a fresh comfortable layout")
	}
}

// The busy row keeps the clocks and the stop key; the footer drops the
// context bar.
func TestCompactStatusRow(t *testing.T) {
	m := goldenTurnModel(t, 120, 40)
	m.agent.ContextLimit = 1000
	full := ansi.Strip(m.busyRow())
	foot := ansi.Strip(m.infoRow())
	useCompact(t)
	terse := ansi.Strip(m.busyRow())
	if !strings.Contains(full, "Esc to interrupt") || strings.Contains(terse, "interrupt") || !strings.Contains(terse, "Esc") {
		t.Errorf("busy row: comfortable %q, compact %q", full, terse)
	}
	if lipgloss.Width(terse) > lipgloss.Width(full) {
		t.Errorf("compact busy row should not be wider")
	}
	if got := ansi.Strip(m.infoRow()); strings.Contains(got, glyphs.BarEmpty) || !strings.Contains(foot, glyphs.BarEmpty) {
		t.Errorf("footer: comfortable %q, compact %q", foot, got)
	}
}

// Cards lose their frame but keep their title, tone and text column.
func TestCompactCard(t *testing.T) {
	framed := renderCard(tokStateWarn, "Allow?", []string{"run go build", "  1. Yes"}, 40)
	useCompact(t)
	bare := renderCard(tokStateWarn, "Allow?", []string{"run go build", "  1. Yes"}, 40)
	fl, bl := strings.Split(ansi.Strip(framed), "\n"), strings.Split(ansi.Strip(bare), "\n")
	if len(bl) != len(fl)-1 {
		t.Fatalf("compact card drops the bottom border only: %d vs %d rows\n%s", len(bl), len(fl), ansi.Strip(bare))
	}
	if strings.ContainsAny(ansi.Strip(bare), "╭╮╰╯│") {
		t.Errorf("compact card has frame glyphs:\n%s", ansi.Strip(bare))
	}
	if !strings.Contains(bl[0], "Allow?") || strings.Index(bl[1], "run") != strings.Index(fl[1], "run") {
		t.Errorf("title and text column should stay:\n%s\n%s", ansi.Strip(framed), ansi.Strip(bare))
	}
}

// TestCompactGoldens pins the representative screens in compact density.
func TestCompactGoldens(t *testing.T) {
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
	useCompact(t)

	modes := []struct {
		name  string
		ascii bool
	}{{"truecolor-dark", false}, {"ascii", true}}
	for _, scene := range goldenScenes[1:] {
		for _, mode := range modes {
			for _, size := range [][2]int{{80, 24}, {120, 40}} {
				name := fmt.Sprintf("compact-%s_%s_%dx%d", scene.name, mode.name, size[0], size[1])
				t.Run(name, func(t *testing.T) {
					restore := useProfile(t, termenv.TrueColor, true)
					defer restore()
					SetLightTheme(false)
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
							t.Errorf("row %d is %d cells wide on a %d-column screen", i, w, size[0])
						}
					}
					checkGolden(t, name+".ansi", view)
					checkGolden(t, name+".txt", stripView(view))
				})
			}
		}
	}
}
