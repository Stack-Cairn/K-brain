package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

func TestMaxContentWidthCapsTranscript(t *testing.T) {
	m := surfaceModel(t)
	m.Update(mkWinSize(120, 40))
	mixedTranscript(m)
	if got := m.layoutWidth(); got != 120 {
		t.Fatalf("off by default: layout width %d", got)
	}
	m.setMaxContentWidth(72)
	if got := m.layoutWidth(); got != 72 {
		t.Fatalf("capped layout width %d", got)
	}
	checkIndex(t, m, "capped")
	for i, r := range m.tx.ix.rows(0, m.transcriptRows()) {
		if w := lipgloss.Width(r); w > 72 {
			t.Fatalf("row %d is %d wide: %q", i, w, ansi.Strip(r))
		}
	}
	saved, err := config.Load()
	if err != nil || saved.MaxContentWidth != 72 {
		t.Fatalf("saved maxContentWidth %d, %v", saved.MaxContentWidth, err)
	}

	// A window narrower than the cap is not affected; a tiny cap is raised.
	m.Update(mkWinSize(60, 40))
	if got := m.layoutWidth(); got != 60 {
		t.Errorf("narrow window: layout width %d", got)
	}
	m.setMaxContentWidth(10)
	m.Update(mkWinSize(120, 40))
	if got := m.layoutWidth(); got != minContentWidth {
		t.Errorf("tiny cap: layout width %d, want %d", got, minContentWidth)
	}

	// The streaming reply wraps at the cap too.
	m.setMaxContentWidth(50)
	m.busy = true
	m.current = strings.Repeat("word ", 60)
	for _, l := range strings.Split(m.currentView(), "\n") {
		if w := lipgloss.Width(l); w > 50 {
			t.Fatalf("live reply row %d wide: %q", w, ansi.Strip(l))
		}
	}
	m.busy, m.current = false, ""

	// Back to off restores the full width.
	m.setMaxContentWidth(0)
	if got := m.layoutWidth(); got != 120 {
		t.Errorf("off again: layout width %d", got)
	}
	checkIndex(t, m, "off")
}

func TestContentWidthSettingsRow(t *testing.T) {
	m := surfaceModel(t)
	var row settingDef
	for _, d := range settingDefs() {
		if d.name == "Content width" {
			row = d
		}
	}
	if row.value == nil || row.value(m) != "off" {
		t.Fatal("the row starts off")
	}
	var seen []string
	for range contentWidthSteps {
		row.step(m, 1)
		seen = append(seen, row.value(m))
	}
	if got := strings.Join(seen, ","); got != "80 columns,100 columns,120 columns,160 columns,off" {
		t.Errorf("→ walks %s", got)
	}
	row.step(m, -1)
	if row.value(m) != "160 columns" {
		t.Errorf("← from off wraps to the widest: %s", row.value(m))
	}
	// A hand-written value steps to its neighbours.
	m.cfg.MaxContentWidth = 90
	row.step(m, 1)
	if m.cfg.MaxContentWidth != 100 {
		t.Errorf("90 → %d, want 100", m.cfg.MaxContentWidth)
	}
	m.cfg.MaxContentWidth = 90
	row.step(m, -1)
	if m.cfg.MaxContentWidth != 80 {
		t.Errorf("90 ← %d, want 80", m.cfg.MaxContentWidth)
	}
}

// TestContentWidthGoldens pins the representative turn on a wide window
// with the transcript capped at 80 columns.
func TestContentWidthGoldens(t *testing.T) {
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
	for _, mode := range []struct {
		name  string
		ascii bool
	}{{"truecolor-dark", false}, {"ascii", true}} {
		name := fmt.Sprintf("maxwidth80-turn_%s_120x40", mode.name)
		t.Run(name, func(t *testing.T) {
			restore := useProfile(t, termenv.TrueColor, true)
			defer restore()
			SetLightTheme(false)
			setASCIIGlyphs(mode.ascii)
			clock := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
			toolTimer.now = func() time.Time { return clock }
			m := compactCmdModel()
			m.cfg.MaxContentWidth = 80
			m.now = func() time.Time { return clock }
			m.permissionMode = "always"
			m.applyAppearance()
			m.Update(mkWinSize(120, 40))
			goldenTurn(m, &clock)
			m.layout()
			view := m.View()
			checkGolden(t, name+".ansi", view)
			checkGolden(t, name+".txt", stripView(view))
		})
	}
}
