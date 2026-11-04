package tui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
)

func statusModel() *model {
	m := newGrowModel()
	m.agent = &agent.Agent{}
	return m
}

func TestInfoRowShowsModelEffortAndSpend(t *testing.T) {
	m := statusModel()
	m.modelName = "kimi-k3-fast"
	m.provName = "inference"
	m.agent.Effort = "high"
	m.agent.AddUsage(ai.Usage{PromptTokens: 45230, CompletionTokens: 3120})

	v := m.View()
	for _, want := range []string{"kimi-k3-fast", "✦ high", "48.4k tok"} {
		if !strings.Contains(v, want) {
			t.Errorf("info row should show %q\n--- view tail ---\n%s", want, tailLines(v, 4))
		}
	}
	if strings.Contains(v, "inference") {
		t.Errorf("the provider no longer crowds the info row\n%s", tailLines(v, 4))
	}
}

func TestInfoRowDefaults(t *testing.T) {
	m := statusModel()
	m.modelName = "m"
	m.provName = "p"

	hints := ansi.Strip(m.footerHints())
	if !strings.HasSuffix(hints, "m · ✦ off") {
		t.Errorf("model and effort should close the info row, got %q", hints)
	}
	for _, noise := range []string{"tok", "$", "↑", "sub"} {
		if strings.Contains(hints, noise) {
			t.Errorf("an idle session should not show %q: %q", noise, hints)
		}
	}
}

func TestInfoRowIsLastRowBelowComposer(t *testing.T) {
	m := statusModel()
	m.modelName = "m"
	m.provName = "p"
	m.escClr = true

	lines := strings.Split(strings.TrimRight(m.View(), "\n"), "\n")
	inputRow, infoRow := -1, -1
	for i, l := range lines {
		if strings.Contains(l, "Ask k-brain anything") {
			inputRow = i
		}
		if strings.Contains(l, "✦ off") {
			infoRow = i
		}
	}
	if inputRow < 1 || infoRow != inputRow+1 {
		t.Fatalf("info row should sit directly under the input (input=%d info=%d)\n%s", inputRow, infoRow, m.View())
	}
	if got := ansi.Strip(lines[inputRow-1]); strings.Trim(strings.TrimSpace(got), "─") != "" {
		t.Errorf("want the composer rule directly above the input, got %q", got)
	}
	if infoRow != len(lines)-1 {
		t.Errorf("info row should be the last row (row %d of %d lines)", infoRow, len(lines)-1)
	}
}

func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func TestInfoRowTruncatesGoalNotModel(t *testing.T) {
	for _, width := range []int{20, 40, 58, 120} {
		m := statusModel()
		m.width = width
		m.modelName = "kimi-k3-fast"
		m.agent.Effort = "high"
		m.goal = strings.Repeat("ship the release ", 10)
		m.agent.AddUsage(ai.Usage{PromptTokens: 45230, CompletionTokens: 3120})
		line := ansi.Strip(m.footerHints())
		if got := lipgloss.Width(line); got > width {
			t.Fatalf("width %d: info row is %d wide: %q", width, got, line)
		}
		if width >= 58 && !strings.HasSuffix(line, "kimi-k3-fast · ✦ high") {
			t.Fatalf("width %d: model and effort must survive goal truncation: %q", width, line)
		}
	}
}

func TestInfoRowShowsCost(t *testing.T) {
	m := statusModel()
	m.modelName = "m"
	m.provName = "p"
	m.agent.Model = "priced"
	m.catalogs = map[string]config.Catalog{
		"p": {Models: []config.ModelInfoLite{{ID: "priced", Pricing: &ai.TokenRates{Input: 1e-6, Output: 5e-6, CacheRead: 1e-7, CacheWrite: 1e-6}}}},
	}
	u := ai.Usage{PromptTokens: 10000, CompletionTokens: 1000}
	u.PromptTokensDetails = &struct {
		CachedTokens int `json:"cached_tokens"`
	}{CachedTokens: 8000}
	m.agent.AddUsage(u)

	got := m.footerHints()
	if !strings.Contains(got, "$0.0078") {
		t.Errorf("cost should show in the info row: %q", got)
	}
	if strings.Contains(got, "tok") {
		t.Errorf("a priced session shows cost instead of tokens: %q", got)
	}
}

func TestInfoRowHidesCostWithoutPricing(t *testing.T) {
	m := statusModel()
	m.modelName = "m"
	m.provName = "p"
	m.agent.Model = "unpriced"
	m.catalogs = map[string]config.Catalog{
		"p": {Models: []config.ModelInfoLite{{ID: "unpriced"}}},
	}
	m.agent.AddUsage(ai.Usage{PromptTokens: 10000, CompletionTokens: 1000})

	if got := m.footerHints(); strings.Contains(got, "$") || !strings.Contains(got, "11.0k tok") {
		t.Errorf("unpriced model should fall back to a token count: %q", got)
	}

	m.catalogs = nil
	if got := m.footerHints(); strings.Contains(got, "$") {
		t.Errorf("missing catalog should hide cost: %q", got)
	}
}

func TestInfoRowShowsScrollOnlyWhenScrolledBack(t *testing.T) {
	m := statusModel()
	m.modelName = "m"
	m.follow = true
	if got := ansi.Strip(m.footerHints()); strings.Contains(got, "↑") {
		t.Errorf("following the tail should not show a scroll position: %q", got)
	}
	m.follow = false
	if got := ansi.Strip(m.footerHints()); strings.Contains(got, "↑") {
		t.Errorf("sitting on the last row is not scrolled back: %q", got)
	}
	for i := range 200 {
		m.append("row " + strings.Repeat("x", i%7))
	}
	m.layout()
	m.follow = false
	m.vp.GotoTop()
	if got := ansi.Strip(m.footerHints()); !strings.Contains(got, "↑ 0%") {
		t.Errorf("scrolled back should show the scroll position: %q", got)
	}
}

func TestFmtCost(t *testing.T) {
	if got := fmtCost(0.0134); got != "$0.0134" {
		t.Errorf("sub-dollar: %q", got)
	}
	if got := fmtCost(12.345); got != "$12.35" {
		t.Errorf("over a dollar: %q", got)
	}
}

func TestSessionCostUsesFetchedPricing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[
			{"id":"kimi-k3","pricing":{"prompt":"0.000003","completion":"0.000015","input_cache_read":"0.0000003"}},
			{"id":"kimi-k3-fast","pricing":{"prompt":"0.0000045","completion":"0.0000225","input_cache_read":"0.00000045"}}
		]}`))
	}))
	defer srv.Close()

	infos, err := ai.New(srv.URL, "k").Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	lites := make([]config.ModelInfoLite, len(infos))
	for i, mi := range infos {
		lites[i] = config.ModelInfoLite{ID: mi.ID}
		if mi.Pricing != nil {
			lites[i].Pricing = mi.Pricing.Rates()
		}
	}
	m := statusModel()
	m.provName = "inference"
	m.catalogs = map[string]config.Catalog{"inference": {Models: lites}}
	u := ai.Usage{PromptTokens: 31100, CompletionTokens: 360}
	u.PromptTokensDetails = &struct {
		CachedTokens int `json:"cached_tokens"`
	}{CachedTokens: 20700}
	m.agent.Model = "kimi-k3-fast"
	m.agent.SetUsage(u)
	fast, ok := m.sessionCost()
	if !ok {
		t.Fatal("fast variant should be priced")
	}
	m.agent.Model = "kimi-k3"
	m.agent.SetUsage(u)
	std, ok := m.sessionCost()
	if !ok {
		t.Fatal("standard variant should be priced")
	}
	if fast <= std {
		t.Errorf("kimi-k3-fast cost %v should exceed kimi-k3 %v", fast, std)
	}

	if want := 0.064215; fast-want > 1e-12 || want-fast > 1e-12 {
		t.Errorf("kimi-k3-fast cost = %v, want %v", fast, want)
	}

	m.agent.AddSubUsage("kimi-k3-fast @ ", ai.Usage{PromptTokens: 1000, CompletionTokens: 100})
	withSub, ok := m.sessionCost()
	if !ok {
		t.Fatal("sub under a known model should be priced")
	}
	subCost := 1000*4.5e-6 + 100*22.5e-6
	if diff := withSub - std - subCost; diff > 1e-12 || diff < -1e-12 {
		t.Errorf("cost with sub = %v, want own %v + sub %v", withSub, std, subCost)
	}

	m.agent.AddSubUsage("mystery-model @ elsewhere", ai.Usage{PromptTokens: 1})
	if _, ok := m.sessionCost(); ok {
		t.Error("an unpriceable sub must hide the cost segment")
	}
}
