package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

func TestPaletteOpensAndClosesOnEsc(t *testing.T) {
	m := compactCmdModel()
	tm, _ := m.key(tea.KeyMsg{Type: tea.KeyCtrlP})
	m = tm.(*model)
	if m.palette == nil {
		t.Fatal("ctrl+p should open the palette")
	}

	tm, _ = m.key(tea.KeyMsg{Type: tea.KeyEsc})
	m = tm.(*model)
	if m.palette != nil {
		t.Fatal("esc should close the palette")
	}
}

func TestPaletteSuggestedGroupOnTop(t *testing.T) {
	m := compactCmdModel()
	m.openPalette()
	if m.palette.items[0].category != "Suggested" {
		t.Fatalf("empty filter should pin a Suggested group, got %q", m.palette.items[0].category)
	}
	titles := map[string]bool{}
	for _, it := range m.palette.items {
		titles[it.title] = true
	}
	for _, want := range []string{"Model", "Resume session", "Compact session", "Goal", "Help", "Quit"} {
		if !titles[want] {
			t.Errorf("palette missing %q", want)
		}
	}
}

func TestPaletteFilter(t *testing.T) {
	m := compactCmdModel()
	m.openPalette()
	for _, r := range "new sess" {
		tm, _ := m.paletteKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = tm.(*model)
	}
	if len(m.palette.items) != 1 || m.palette.items[0].title != "New session" {
		t.Fatalf("filter 'new sess': %+v", m.palette.items)
	}
	if m.palette.items[0].category != "Session" {
		t.Fatalf("filtering drops the Suggested group, got %q", m.palette.items[0].category)
	}

	for range 8 {
		tm, _ := m.paletteKey(tea.KeyMsg{Type: tea.KeyBackspace})
		m = tm.(*model)
	}
	if len(m.palette.items) < 10 {
		t.Fatalf("backspace should restore all items, got %d", len(m.palette.items))
	}
}

func TestPaletteNavigationWraps(t *testing.T) {
	m := compactCmdModel()
	m.openPalette()
	n := len(m.palette.items)

	tm, _ := m.paletteKey(tea.KeyMsg{Type: tea.KeyUp})
	m = tm.(*model)
	if m.palette.idx != n-1 {
		t.Fatalf("up from 0 should wrap to %d, got %d", n-1, m.palette.idx)
	}

	tm, _ = m.paletteKey(tea.KeyMsg{Type: tea.KeyDown})
	m = tm.(*model)
	if m.palette.idx != 0 {
		t.Fatalf("down should wrap to 0, got %d", m.palette.idx)
	}
}

func TestPaletteEnterRunsCommand(t *testing.T) {
	m := compactCmdModel()
	m.openPalette()
	for _, r := range "quit" {
		tm, _ := m.paletteKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = tm.(*model)
	}
	_, cmd := m.paletteKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Quit should return tea.Quit")
	}
	if msg := cmd(); msg != tea.Quit() {
		t.Fatalf("expected tea.QuitMsg, got %v", msg)
	}
}

func TestPaletteViewRendersCategories(t *testing.T) {
	m := compactCmdModel()
	m.openPalette()
	m.width = 100
	v := m.paletteView()
	for _, want := range []string{"Commands", "Suggested", "Agent", "Session", "Display", "App", "esc close"} {
		if !strings.Contains(v, want) {
			t.Errorf("palette view missing %q:\n%s", want, v)
		}
	}
}

func TestPaletteCtrlCClosesNotQuits(t *testing.T) {
	m := compactCmdModel()
	m.openPalette()
	tm, _ := m.key(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = tm.(*model)
	if m.palette != nil {
		t.Fatal("ctrl+c should close the palette, not quit the app")
	}
}

func TestPaletteArrowsStepEffortInPlace(t *testing.T) {
	m := compactCmdModel()
	m.openPalette()
	var tm tea.Model
	for _, r := range "effort" {
		tm, _ = m.paletteKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = tm.(*model)
	}
	if m.palette.items[m.palette.idx].title != "Reasoning effort" {
		t.Fatalf("filter 'effort' should select Reasoning effort")
	}
	tm, _ = m.paletteKey(tea.KeyMsg{Type: tea.KeyRight})
	m = tm.(*model)
	if m.palette == nil {
		t.Fatal("→ must keep the palette open")
	}
	if m.agent.Effort != "low" {
		t.Fatalf("→ should step off → low, got %q", m.agent.Effort)
	}
	tm, _ = m.paletteKey(tea.KeyMsg{Type: tea.KeyLeft})
	m = tm.(*model)
	if m.agent.Effort != "" {
		t.Fatalf("← should step back to off, got %q", m.agent.Effort)
	}
}

func TestPaletteToggleThinkingInPlace(t *testing.T) {
	m := compactCmdModel()
	m.showThinking = true
	m.openPalette()
	var tm tea.Model
	for _, r := range "thinking" {
		tm, _ = m.paletteKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = tm.(*model)
	}
	tm, _ = m.paletteKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = tm.(*model)
	if m.palette == nil {
		t.Fatal("enter on a toggle must keep the palette open")
	}
	if m.showThinking {
		t.Fatal("enter should have toggled thinking tokens off")
	}

	reloaded, err := config.Load()
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if reloaded.Thinking == nil || *reloaded.Thinking {
		t.Fatalf("expected thinking: false saved to config, got %v", reloaded.Thinking)
	}
	tm, _ = m.paletteKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = tm.(*model)
	if !m.showThinking {
		t.Fatal("a second enter should toggle thinking tokens back on")
	}
	reloaded, err = config.Load()
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if reloaded.Thinking == nil || !*reloaded.Thinking {
		t.Fatalf("expected thinking: true saved to config, got %v", reloaded.Thinking)
	}
}

func TestPalettePanelPushPop(t *testing.T) {
	m := compactCmdModel()
	m.openPalette()
	var tm tea.Model
	for _, r := range "effort" {
		tm, _ = m.paletteKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = tm.(*model)
	}
	tm, _ = m.paletteKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = tm.(*model)
	pp := m.palette.top()
	if pp == nil || pp.kind != panelEffort {
		t.Fatal("enter should push the effort panel")
	}
	if pp.levels[pp.lidx] != m.agent.Effort {
		t.Fatalf("panel should start on the current level, got %q", pp.levels[pp.lidx])
	}

	tm, _ = m.paletteKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	m = tm.(*model)
	if m.palette.filter != "effort" {
		t.Fatalf("panel should not edit the root filter, got %q", m.palette.filter)
	}
	tm, _ = m.paletteKey(tea.KeyMsg{Type: tea.KeyEsc})
	m = tm.(*model)
	if m.palette == nil || m.palette.top() != nil {
		t.Fatal("esc should pop back to the root list, not close")
	}
}

func TestPaletteEffortPanelApplies(t *testing.T) {
	m := compactCmdModel()
	m.openPalette()
	var tm tea.Model
	for _, r := range "effort" {
		tm, _ = m.paletteKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = tm.(*model)
	}
	tm, _ = m.paletteKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = tm.(*model)
	tm, _ = m.paletteKey(tea.KeyMsg{Type: tea.KeyDown})
	m = tm.(*model)
	tm, _ = m.paletteKey(tea.KeyMsg{Type: tea.KeyDown})
	m = tm.(*model)
	tm, _ = m.paletteKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = tm.(*model)
	if m.agent.Effort != "medium" {
		t.Fatalf("enter should apply the highlighted level, got %q", m.agent.Effort)
	}
	if m.palette.top() != nil {
		t.Fatal("enter should pop the panel after applying")
	}
}

func TestPaletteModelPanelPreviewsLive(t *testing.T) {
	m := compactCmdModel()
	m.openPalette()
	var tm tea.Model
	tm, _ = m.paletteKey(tea.KeyMsg{Type: tea.KeyDown})
	m = tm.(*model)
	tm, _ = m.paletteKey(tea.KeyMsg{Type: tea.KeyUp})
	m = tm.(*model)
	tm, _ = m.paletteKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = tm.(*model)
	pp := m.palette.top()
	if pp == nil || pp.kind != panelModel {
		t.Fatal("enter should push the model panel")
	}
	if len(pp.items) != 3 {
		t.Fatalf("expected 3 routes, got %d", len(pp.items))
	}
	tm, _ = m.paletteKey(tea.KeyMsg{Type: tea.KeyDown})
	m = tm.(*model)
	if m.modelName != config.DefaultCompactModel {
		t.Fatalf("browsing should live-preview the switch, got %q", m.modelName)
	}
	if m.cfg.DefaultModel != "kimi-k3-fast" {
		t.Fatal("preview must not persist the default before enter")
	}

	tm, _ = m.paletteKey(tea.KeyMsg{Type: tea.KeyEsc})
	m = tm.(*model)
	if m.palette.top() != nil {
		t.Fatal("esc should pop the model panel")
	}
}

func TestPaletteGoalPanelSetsGoal(t *testing.T) {
	m := compactCmdModel()

	m.prog = tea.NewProgram(m, tea.WithoutRenderer())
	defer m.prog.Kill()
	m.openPalette()
	var tm tea.Model
	for _, r := range "goal" {
		tm, _ = m.paletteKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = tm.(*model)
	}
	tm, _ = m.paletteKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = tm.(*model)
	for _, r := range "ship it" {
		tm, _ := m.paletteKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = tm.(*model)
	}
	tm, _ = m.paletteKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = tm.(*model)
	if m.goal != "ship it" {
		t.Fatalf("enter should set the goal, got %q", m.goal)
	}
	if !m.busy {
		t.Fatal("setting a goal should start the first turn")
	}
	if m.palette.top() != nil {
		t.Fatal("enter should pop the goal panel")
	}
}

func TestPaletteCompactPanelAppliesInPlace(t *testing.T) {
	m := compactCmdModel()
	m.openPalette()
	var tm tea.Model
	for m.palette.items[m.palette.idx].title != "Compaction model" {
		tm, _ = m.paletteKey(tea.KeyMsg{Type: tea.KeyDown})
		m = tm.(*model)
	}
	tm, _ = m.paletteKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = tm.(*model)
	pp := m.palette.top()
	if pp == nil || pp.kind != panelCompact {
		t.Fatal("enter should push the compaction panel")
	}
	if pp.midx != 0 {
		t.Fatalf("should start on the default row, got %d", pp.midx)
	}
	tm, _ = m.paletteKey(tea.KeyMsg{Type: tea.KeyDown})
	m = tm.(*model)
	tm, _ = m.paletteKey(tea.KeyMsg{Type: tea.KeyRight})
	m = tm.(*model)
	if m.compactModel == "" {
		t.Fatal("→ should apply the highlighted model")
	}
	if m.palette.top() == nil {
		t.Fatal("→ must keep the panel open")
	}
}

func TestPaletteCompactPanelDefaultRowRestores(t *testing.T) {
	m := compactCmdModel()
	m.compactCommand([]string{"glm-5.2-fast"})
	m.openPaletteOn("Compaction model")
	pp := m.palette.top()
	if pp == nil || pp.kind != panelCompact {
		t.Fatal("openPaletteOn should land in the compaction panel")
	}
	if !strings.Contains(pp.list[0], "default (") {
		t.Fatalf("first row should read default (…), got %q", pp.list[0])
	}
	for pp.midx != 0 {
		tm, _ := m.paletteKey(tea.KeyMsg{Type: tea.KeyUp})
		m = tm.(*model)
	}
	tm, _ := m.paletteKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = tm.(*model)
	if m.compactModel != "" || m.agent.CompactModel != config.DefaultCompactModel {
		t.Fatalf("the default row should restore the built-in default: %q / %q", m.compactModel, m.agent.CompactModel)
	}

	if m.palette != nil && m.palette.top() != nil {
		t.Fatal("enter should pop the panel")
	}
}

func TestPaletteModelPanelFilters(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	if err := config.SaveCatalogs(map[string]config.Catalog{
		"inference": {FetchedAt: time.Now(), Models: []config.ModelInfoLite{{ID: "deepseek-v4-pro", ContextLength: 1048576}}},
	}); err != nil {
		t.Fatal(err)
	}
	m := compactCmdModel()
	m.openPaletteOn("Compaction model")
	pp := m.palette.top()
	full := len(pp.list)
	if full < 2 {
		t.Fatalf("fixture should list several models, got %v", pp.list)
	}
	tm, _ := m.paletteKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v4-pro")})
	m = tm.(*model)
	pp = m.palette.top()
	view := pp.filter.view(len(pp.list))
	if len(view) != 1 {
		var rows []string
		for _, r := range view {
			rows = append(rows, pp.list[r])
		}
		t.Fatalf("typing should narrow %d rows to the v4-pro match, got %v", full, rows)
	}
	if name := pp.list[view[0]]; !strings.HasPrefix(name, "deepseek-v4-pro") {
		t.Fatalf("the surviving row should be the catalog model, got %q", name)
	}

	for len(pp.filter.query) > 0 {
		tm, _ = m.paletteKey(tea.KeyMsg{Type: tea.KeyBackspace})
		m = tm.(*model)
		pp = m.palette.top()
	}
	if len(pp.filter.view(len(pp.list))) != full {
		t.Fatalf("clearing the query should restore all %d rows, got %d", full, len(pp.filter.view(len(pp.list))))
	}

	tm, _ = m.paletteKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v4-pro")})
	m = tm.(*model)
	tm, _ = m.paletteKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = tm.(*model)
	if m.compactModel != "deepseek-v4-pro" {
		t.Fatalf("enter should apply the filtered model, got %q", m.compactModel)
	}
}

func TestPaletteModelPanelFilterNoMatch(t *testing.T) {
	m := compactCmdModel()
	m.openPaletteOn("Compaction model")
	tm, _ := m.paletteKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("zzz-no-such")})
	m = tm.(*model)
	pp := m.palette.top()
	if len(pp.filter.view(len(pp.list))) != 0 {
		t.Fatal("a nonsense query should match nothing")
	}
	view := m.panelView(pp)
	if !strings.Contains(view, "no models match") {
		t.Errorf("the empty-filter view should say so, got %q", view)
	}

	tm, _ = m.paletteKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = tm.(*model)
	if m.compactModel != "" {
		t.Errorf("an empty view must not change the compaction model, got %q", m.compactModel)
	}
}

func TestPaletteCompactPanelListsCatalogModels(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	if err := config.SaveCatalogs(map[string]config.Catalog{
		"inference": {FetchedAt: time.Now(), Models: []config.ModelInfoLite{{ID: "deepseek-v4-pro", ContextLength: 1048576}}},
	}); err != nil {
		t.Fatal(err)
	}
	m := compactCmdModel()
	m.openPaletteOn("Compaction model")
	pp := m.palette.top()
	if pp == nil || pp.kind != panelCompact {
		t.Fatal("openPaletteOn should land in the compaction panel")
	}
	found := -1
	for i, name := range pp.list {
		if strings.HasPrefix(name, "deepseek-v4-pro") {
			found = i
			if !strings.HasSuffix(name, dimNew) {
				t.Fatalf("the catalog row should carry the (new) marker, got %q", name)
			}
		}
	}
	if found < 0 {
		t.Fatalf("catalog models should be listed, got %v", pp.list)
	}
	for pp.midx != found {
		tm, _ := m.paletteKey(tea.KeyMsg{Type: tea.KeyDown})
		m = tm.(*model)
	}
	tm, _ := m.paletteKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = tm.(*model)
	if m.compactModel != "deepseek-v4-pro" || m.agent.CompactModel != "deepseek-v4-pro" {
		t.Fatalf("enter should pick the catalog model, got %q / %q", m.compactModel, m.agent.CompactModel)
	}
	if _, ok := m.cfg.Models["deepseek-v4-pro"]; ok {
		t.Error("picking a catalog model must not write it into cfg.Models")
	}
}

func TestPaletteCompactionLevelSteps(t *testing.T) {
	m := compactCmdModel()
	m.agent.CompactThreshold = compactThresholdFor(m.cfg)
	m.openPalette()
	var it *paletteItem
	for i := range m.palette.items {
		if m.palette.items[i].title == "Compaction level" {
			it = &m.palette.items[i]
			break
		}
	}
	if it == nil {
		t.Fatal("palette should have a Compaction level row")
	}
	if it.stepFwd == nil || it.stepBack == nil {
		t.Fatal("Compaction level should be ←/→ steppable")
	}
	it.stepFwd(m)
	if m.agent.CompactThreshold != 0.6 {
		t.Fatalf("→ should step to 60%%, got %v", m.agent.CompactThreshold)
	}
	it.stepBack(m)
	it.stepBack(m)
	if m.agent.CompactThreshold != 0.4 {
		t.Fatalf("← ← should step to 40%%, got %v", m.agent.CompactThreshold)
	}
	if state := paletteState(m, *it); !strings.Contains(state, "40%") {
		t.Fatalf("the row badge should show the live level, got %q", state)
	}
}
