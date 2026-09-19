package tui

import (
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func TestEffortCycleAndParse(t *testing.T) {
	got := ""
	for _, want := range []string{"low", "medium", "high", "", "low"} {
		got = nextEffort(defaultEfforts, got)
		if got != want {
			t.Fatalf("cycle: got %q want %q", got, want)
		}
	}
	if nextEffort(defaultEfforts, "bogus") != "" {
		t.Fatal("unknown level should reset to off")
	}
	if effortLabel("") != "off" || effortLabel("high") != "high" {
		t.Fatal("labels")
	}
	for in, want := range map[string]string{"off": "", "low": "low", "high": "high"} {
		if lv, ok := parseEffort(defaultEfforts, in); !ok || lv != want {
			t.Fatalf("parse %q: %q %v", in, lv, ok)
		}
	}
	if _, ok := parseEffort(defaultEfforts, "ultra"); ok {
		t.Fatal("invalid level accepted")
	}
}

func TestEffortCompletion(t *testing.T) {
	_, cs := completions("/effort h", nil, nil, nil, nil)
	if len(cs) != 1 || cs[0].Text != "high" {
		t.Fatalf("effort completion: %v", texts(cs))
	}
}

func TestEffortsForAdvertisedLevels(t *testing.T) {
	m := &model{
		provName: "inference",
		agent:    &agent.Agent{Model: "deepseek-v4-flash"},
		catalogs: map[string]config.Catalog{
			"inference": {Models: []config.ModelInfoLite{
				{ID: "deepseek-v4-flash", ReasoningEfforts: []string{"low", "high", "max"}},
				{ID: "claude-opus-5", ReasoningEfforts: []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}},
				{ID: "gemini-3.5-flash"},
			}},
		},
	}
	if got := m.effortsFor(); len(got) != 4 || got[0] != "" || got[3] != "max" {
		t.Fatalf("advertised levels: %v", got)
	}
	if next := nextEffort(m.effortsFor(), "high"); next != "max" {
		t.Fatalf("cycle should reach max: %q", next)
	}
	if _, ok := parseEffort(m.effortsFor(), "medium"); ok {
		t.Fatal("medium should be rejected for deepseek")
	}

	m.agent.Model = "claude-opus-5"
	got := m.effortsFor()
	if got[0] != "" || len(got) != 7 {
		t.Fatalf("claude levels: %v", got)
	}
	for _, e := range got {
		if e == "none" {
			t.Fatalf("none should map to off: %v", got)
		}
	}

	m.agent.Model = "gemini-3.5-flash"
	if got := m.effortsFor(); len(got) != len(defaultEfforts) {
		t.Fatalf("gemini should fall back to defaults: %v", got)
	}

	m.provName = "elsewhere"
	if got := m.effortsFor(); len(got) != len(defaultEfforts) {
		t.Fatalf("missing catalog should fall back to defaults: %v", got)
	}
}

func TestEffortBareOpensSelector(t *testing.T) {
	m := compactCmdModel()
	m.command("/effort")
	if m.palette == nil {
		t.Fatal("bare /effort should open the palette")
	}
	pp := m.palette.top()
	if pp == nil || pp.kind != panelEffort {
		t.Fatalf("expected the effort panel, got %+v", pp)
	}
	if len(pp.levels) != len(defaultEfforts) || pp.levels[pp.lidx] != m.agent.Effort {
		t.Fatalf("effort panel should list the model's levels on the current one: %v @%d", pp.levels, pp.lidx)
	}

	tm, _ := m.paletteKey(tea.KeyMsg{Type: tea.KeyDown})
	m = tm.(*model)
	tm, _ = m.paletteKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = tm.(*model)
	if m.agent.Effort != "low" {
		t.Fatalf("selecting low in the selector should apply it, got %q", m.agent.Effort)
	}

	if m.palette != nil {
		t.Fatal("enter in a directly-opened selector should close the palette")
	}
}

func TestSetEffortPersistsGlobalAndSession(t *testing.T) {
	m := compactCmdModel()
	m.cfg.DefaultEffort = "medium"
	m.agent.Effort = "medium"
	st, err := session.Open(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	m.store = st
	id, err := st.Create("/tmp", m.modelName, m.provName)
	if err != nil {
		t.Fatal(err)
	}
	m.sessionID = id

	m.setEffort("low")
	if m.agent.Effort != "low" {
		t.Fatalf("agent effort: %q", m.agent.Effort)
	}
	reloaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.DefaultEffort != "low" {
		t.Fatalf("global default should follow the pick, got %q", reloaded.DefaultEffort)
	}
	meta, _, err := st.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Effort != "low" {
		t.Fatalf("session row should carry the pick, got %q", meta.Effort)
	}

	m.resetEffort("")
	reloaded, _ = config.Load()
	if reloaded.DefaultEffort != "low" {
		t.Fatalf("reset must not touch the global default, got %q", reloaded.DefaultEffort)
	}
	meta, _, _ = st.Load(id)
	if meta.Effort != "" {
		t.Fatalf("session row should track the reset, got %q", meta.Effort)
	}
}

func TestResumeRestoresEffort(t *testing.T) {
	m := compactCmdModel()
	m.cfg.DefaultEffort = "medium"
	m.agent.Effort = "medium"
	st, err := session.Open(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	m.store = st

	id, err := st.Create("/tmp", m.modelName, m.provName)
	if err != nil {
		t.Fatal(err)
	}
	msgs := []ai.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "q", Authored: true}}
	if err := st.Save(id, 1, msgs, m.modelName, m.provName); err != nil {
		t.Fatal(err)
	}

	if err := st.SetEffort(id, "high"); err != nil {
		t.Fatal(err)
	}
	m.agent.Effort = "low"
	if err := m.resume(id); err != nil {
		t.Fatal(err)
	}
	if m.agent.Effort != "high" {
		t.Fatalf("resume should restore the session effort, got %q", m.agent.Effort)
	}

	id2, err := st.Create("/tmp", m.modelName, m.provName)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Save(id2, 1, msgs, m.modelName, m.provName); err != nil {
		t.Fatal(err)
	}
	m.agent.Effort = "low"
	if err := m.resume(id2); err != nil {
		t.Fatal(err)
	}
	if m.agent.Effort != "low" {
		t.Fatalf("legacy row should inherit the current default, got %q", m.agent.Effort)
	}

	m.persist()
	meta, _, _ := st.Load(id2)
	if meta.Effort != "low" {
		t.Fatalf("persist should stamp the inherited effort, got %q", meta.Effort)
	}
}

func TestResumeRestoresUsage(t *testing.T) {
	m := compactCmdModel()
	st, err := session.Open(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	m.store = st

	id, err := st.Create("/tmp", m.modelName, m.provName)
	if err != nil {
		t.Fatal(err)
	}
	msgs := []ai.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "q", Authored: true}}
	if err := st.Save(id, 1, msgs, m.modelName, m.provName); err != nil {
		t.Fatal(err)
	}
	if err := st.SetUsage(id, 12000, 8000, 1500, nil); err != nil {
		t.Fatal(err)
	}

	if err := m.resume(id); err != nil {
		t.Fatal(err)
	}
	u := m.agent.Usage()
	if u.PromptTokens != 12000 || u.Cached() != 8000 || u.CompletionTokens != 1500 {
		t.Fatalf("resume should restore usage, got in=%d cached=%d out=%d", u.PromptTokens, u.Cached(), u.CompletionTokens)
	}

	m.agent.AddUsage(ai.Usage{PromptTokens: 3000, CompletionTokens: 500})

	m.persist()
	meta, _, _ := st.Load(id)
	if meta.UsageIn != 15000 || meta.UsageOut != 2000 {
		t.Fatalf("persist should store cumulative totals, got in=%d out=%d", meta.UsageIn, meta.UsageOut)
	}

	m.agent.AddSubUsage("sub-m @ p", ai.Usage{PromptTokens: 5000, CompletionTokens: 40})
	m.persist()
	meta, _, _ = st.Load(id)
	if meta.UsageIn != 15000 || meta.UsageOut != 2000 {
		t.Fatalf("sub spend must not leak into the session's own usage columns: %+v", meta)
	}
	if su := meta.SubUsage["sub-m @ p"]; su.PromptTokens != 5000 || su.CompletionTokens != 40 {
		t.Fatalf("sub ledger did not persist: %+v", meta.SubUsage)
	}
	if err := m.resume(id); err != nil {
		t.Fatal(err)
	}
	if su := m.agent.SubUsage()["sub-m @ p"]; su.PromptTokens != 5000 {
		t.Fatalf("resume should restore the sub ledger, got %+v", m.agent.SubUsage())
	}
	if tot := m.agent.TotalUsage(); tot.PromptTokens != 20000 || tot.CompletionTokens != 2040 {
		t.Fatalf("restored total should be own + subs, got %+v", tot)
	}

	id2, err := st.Create("/tmp", m.modelName, m.provName)
	if err != nil {
		t.Fatal(err)
	}
	cu := ai.Usage{
		PromptTokens: 1000, CompletionTokens: 200,
		PromptTokensDetails: &struct {
			CachedTokens int `json:"cached_tokens"`
		}{CachedTokens: 600},
	}
	legacy := []ai.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "q1", Authored: true},
		{Role: "assistant", Content: "a1", Usage: &cu},
		{Role: "user", Content: "q2", Authored: true},
		{Role: "assistant", Content: "a2", Usage: &ai.Usage{PromptTokens: 500, CompletionTokens: 100}},
	}
	if err := st.Save(id2, 1, legacy, m.modelName, m.provName); err != nil {
		t.Fatal(err)
	}
	if err := m.resume(id2); err != nil {
		t.Fatal(err)
	}
	u2 := m.agent.Usage()
	if u2.PromptTokens != 1500 || u2.Cached() != 600 || u2.CompletionTokens != 300 {
		t.Fatalf("legacy row should reconstruct usage from messages, got in=%d cached=%d out=%d",
			u2.PromptTokens, u2.Cached(), u2.CompletionTokens)
	}
	m.persist()
	meta2, _, _ := st.Load(id2)
	if meta2.UsageIn != 1500 || meta2.UsageCached != 600 || meta2.UsageOut != 300 {
		t.Fatalf("persist should stamp the reconstructed totals, got %+v", meta2)
	}

	id3, err := st.Create("/tmp", m.modelName, m.provName)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Save(id3, 1, msgs, m.modelName, m.provName); err != nil {
		t.Fatal(err)
	}
	if err := m.resume(id3); err != nil {
		t.Fatal(err)
	}
	if u := m.agent.Usage(); u.PromptTokens != 0 || u.CompletionTokens != 0 {
		t.Fatalf("usage-free session should start at zero, got %+v", u)
	}
}

func TestUpdateCatalogsResetsUnsupportedEffort(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	m := &model{
		cfg:      &config.Config{},
		provName: "inference",
		agent:    &agent.Agent{Model: "deepseek-v4-flash", Effort: "medium"},
	}
	m.updateCatalogs(map[string]config.Catalog{
		"inference": {Models: []config.ModelInfoLite{
			{ID: "deepseek-v4-flash", ReasoningEfforts: []string{"low", "high", "max"}},
		}},
	})
	if m.agent.Effort != "" {
		t.Fatalf("unsupported effort should reset to off, got %q", m.agent.Effort)
	}

	m.agent.Effort = "high"
	m.updateCatalogs(map[string]config.Catalog{
		"inference": {Models: []config.ModelInfoLite{
			{ID: "deepseek-v4-flash", ReasoningEfforts: []string{"low", "high", "max"}},
		}},
	})
	if m.agent.Effort != "high" {
		t.Fatalf("supported effort should survive, got %q", m.agent.Effort)
	}
}
