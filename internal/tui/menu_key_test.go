package tui

import (
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func pressKey(m *model, kt tea.KeyType) *model {
	tm, _ := m.key(tea.KeyMsg{Type: kt})
	return tm.(*model)
}

func TestTabCompletesSkillName(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	os.MkdirAll(filepath.Join(home, ".k-brain/skills/go-style"), 0o755)
	os.WriteFile(filepath.Join(home, ".k-brain/skills/go-style/SKILL.md"),
		[]byte("---\nname: go-style\ndescription: d\n---\n"), 0o644)
	m := modelCmdModel()
	m = typeStr(t, m, "$go-sty")
	if m.menu == nil || len(m.menu.cands) != 1 || m.menu.cands[0].Text != "$go-style" {
		t.Fatalf("menu should hold exactly $go-style: %+v", m.menu)
	}
	m = pressKey(m, tea.KeyTab)
	if v := m.input.Value(); v != "$go-style" {
		t.Fatalf("tab should complete the skill name, got %q", v)
	}

	m = pressKey(m, tea.KeyTab)
	if v := m.input.Value(); v != "$go-style" {
		t.Fatalf("second tab should not corrupt a single match, got %q", v)
	}
}

func TestTabCyclesWithPreview(t *testing.T) {
	m := modelCmdModel()
	m = typeStr(t, m, "/mode")
	m = pressKey(m, tea.KeyTab)
	first := m.input.Value()
	if first != "/model" && first != "/model-for-session" {
		t.Fatalf("tab should preview a candidate, got %q", first)
	}
	m = pressKey(m, tea.KeyTab)
	second := m.input.Value()
	if first == second {
		t.Fatalf("second tab should preview the next candidate, still %q", first)
	}
	m = pressKey(m, tea.KeyTab)
	if m.input.Value() != first {
		t.Fatalf("wrap should return to %q, got %q", first, m.input.Value())
	}
	m = pressKey(m, tea.KeyShiftTab)
	if m.input.Value() != second {
		t.Fatalf("shift+tab should return to %q, got %q", second, m.input.Value())
	}
}

func TestEscRevertsTabCycle(t *testing.T) {
	m := modelCmdModel()
	m = typeStr(t, m, "/mo")
	m = pressKey(m, tea.KeyTab)
	m = pressKey(m, tea.KeyTab)
	m = pressKey(m, tea.KeyEsc)
	if m.menu != nil {
		t.Fatal("esc should close the menu")
	}
	if m.input.Value() != "/model" {
		t.Fatalf("esc should revert to the cycle base, got %q", m.input.Value())
	}
}

func TestEnterCommitsTabCycle(t *testing.T) {
	m := modelCmdModel()
	m = typeStr(t, m, "/mo")
	m = pressKey(m, tea.KeyTab)
	if m.input.Value() != "/model" {
		t.Fatalf("tab should preview /model first, got %q", m.input.Value())
	}
	m = pressKey(m, tea.KeyEnter)
	if m.mpicker == nil {
		t.Fatal("enter on tab-cycled /model should open the model picker")
	}
	if m.input.Value() != "" {
		t.Fatalf("execNow clears the input, got %q", m.input.Value())
	}

	t.Setenv("HOME", t.TempDir())

	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	m = modelCmdModel()
	m = typeStr(t, m, "/effort ")
	m = pressKey(m, tea.KeyTab)
	want := m.input.Value() + " "
	m = pressKey(m, tea.KeyEnter)
	if m.input.Value() != want {
		t.Fatalf("enter should commit the preview as %q, got %q", want, m.input.Value())
	}
}

func TestArrowsNavigateMenuOnly(t *testing.T) {
	m := modelCmdModel()
	m = typeStr(t, m, "/m")
	idx := m.menu.idx
	m = pressKey(m, tea.KeyDown)
	if m.menu == nil || m.menu.idx == idx {
		t.Fatal("down should move the selection")
	}
	if m.input.Value() != "/m" {
		t.Fatalf("arrows must not edit the input, got %q", m.input.Value())
	}
}

func TestExecNowRunsOnEnterWhileCycling(t *testing.T) {
	m := modelCmdModel()
	m = typeStr(t, m, "/m")
	m = pressKey(m, tea.KeyTab)
	for m.input.Value() != "/mouse" {
		m = pressKey(m, tea.KeyTab)
	}
	m = pressKey(m, tea.KeyEnter)
	if m.input.Value() != "" {
		t.Fatalf("/mouse should run and clear the input, got %q", m.input.Value())
	}
	if !m.mouseOn {
		t.Fatal("/mouse should have toggled mouse capture on")
	}
}
