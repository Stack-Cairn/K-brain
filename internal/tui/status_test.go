package tui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path"
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

func TestStatusLineAlwaysShown(t *testing.T) {
	m := statusModel()
	m.modelName = "kimi-k3-fast"
	m.provName = "inference"
	m.agent.Effort = "high"
	m.agent.AddUsage(ai.Usage{PromptTokens: 45230, CompletionTokens: 3120})

	v := m.View()
	for _, want := range []string{"kimi-k3-fast (high)", "inference", "45.2k", "3.1k"} {
		if !strings.Contains(v, want) {
			t.Errorf("status line should show %q\n--- view tail ---\n%s", want, tailLines(v, 6))
		}
	}

	base := path.Base(cwd())
	if !strings.Contains(v, base) {
		t.Errorf("status line should show the working directory's last segment %q\n%s", base, tailLines(v, 6))
	}
}

func TestStatusLineDefaults(t *testing.T) {
	m := statusModel()
	m.modelName = "m"
	m.provName = "p"

	v := m.View()
	if !strings.Contains(v, "0/0 tok") {
		t.Errorf("empty session should read 0/0 tok\n%s", tailLines(v, 6))
	}
	if strings.Contains(v, "m (") {
		t.Errorf("effort off should not add parens\n%s", tailLines(v, 6))
	}
	if !strings.Contains(v, "  m   p  ") && !strings.Contains(v, " m   p ") {
		t.Errorf("bare model and provider should appear\n%s", tailLines(v, 6))
	}
}

func TestStatusLineShowsCached(t *testing.T) {
	m := statusModel()
	m.modelName = "m"
	m.provName = "p"
	u := ai.Usage{PromptTokens: 10000, CompletionTokens: 500}
	u.PromptTokensDetails = &struct {
		CachedTokens int `json:"cached_tokens"`
	}{CachedTokens: 4000}
	m.agent.AddUsage(u)

	if got := m.statusView(); !strings.Contains(got, "10.0k(4.0k)/500 tok") {
		t.Errorf("cached tokens should show in the spend: %q", got)
	}
}

func TestStatusLineBelowInputAndWarnings(t *testing.T) {
	m := statusModel()
	m.modelName = "m"
	m.provName = "p"
	m.escClr = true

	v := m.View()
	lines := strings.Split(strings.TrimRight(v, "\n"), "\n")
	var inputRow, statusRow int
	for i, l := range lines {
		if strings.Contains(l, "Ask k-brain anything") {
			inputRow = i
		}
		if strings.Contains(l, "0/0 tok") {
			statusRow = i
		}
	}
	if statusRow <= inputRow {
		t.Fatalf("status line should sit below the input (input=%d status=%d)\n%s", inputRow, statusRow, v)
	}
}

func TestStatusLineSpacing(t *testing.T) {
	m := statusModel()
	m.modelName = "m"
	m.provName = "p"

	lines := strings.Split(m.View(), "\n")
	statusRow := -1
	for i, l := range lines {
		if strings.Contains(l, "0/0 tok") {
			statusRow = i
		}
	}
	if statusRow < 1 {
		t.Fatalf("status line not found\n%s", m.View())
	}
	if lines[statusRow-1] != "" {
		t.Errorf("want one blank line above the status line, got %q", lines[statusRow-1])
	}

	if statusRow != len(lines)-1 {
		t.Errorf("status line should be the last row (row %d of %d lines)", statusRow, len(lines)-1)
	}
}

func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func TestStatusLineTruncatesCWDNotSpend(t *testing.T) {
	m := statusModel()
	m.modelName = "kimi-k3-fast"
	m.provName = "inference"
	m.agent.Effort = "high"
	m.agent.AddUsage(ai.Usage{PromptTokens: 45230, CompletionTokens: 3120})

	m.width = 58

	v := m.statusView()
	if !strings.Contains(v, "3.1k") {
		t.Errorf("completion tokens must survive cwd truncation, got %q", v)
	}
	if !strings.Contains(v, "45.2k") {
		t.Errorf("prompt tokens must survive cwd truncation, got %q", v)
	}
	if !strings.Contains(v, "inference") {
		t.Errorf("provider must survive cwd truncation, got %q", v)
	}
}

func TestStatusLineCWDTruncationDisplayWidth(t *testing.T) {
	m := statusModel()
	m.modelName = "m"
	m.provName = "p"
	m.agent.AddUsage(ai.Usage{PromptTokens: 20000, CompletionTokens: 900})
	m.Update(usageMsg(ai.Usage{PromptTokens: 10000, CompletionTokens: 300}))
	m.width = 80

	v := m.statusView()
	if !strings.Contains(v, "last 10.0k/300 tok") {
		t.Errorf("last-response spend (with tok suffix) must survive: %q", v)
	}
	if !strings.Contains(v, "20.0k/900 tok") {
		t.Errorf("session spend must survive: %q", v)
	}
	if w := lipgloss.Width(ansi.Strip(v)); w > m.width {
		t.Errorf("status line width %d exceeds terminal width %d: %q", w, m.width, v)
	}
}

func TestStatusLineShowsCost(t *testing.T) {
	m := statusModel()
	m.modelName = "m"
	m.provName = "p"
	m.agent.Model = "priced"
	m.catalogs = map[string]config.Catalog{
		"p": {Models: []config.ModelInfoLite{{ID: "priced", InPrice: 1e-6, OutPrice: 5e-6, CacheReadPrice: 1e-7}}},
	}
	u := ai.Usage{PromptTokens: 10000, CompletionTokens: 1000}
	u.PromptTokensDetails = &struct {
		CachedTokens int `json:"cached_tokens"`
	}{CachedTokens: 8000}
	m.agent.AddUsage(u)

	if got := m.statusView(); !strings.Contains(got, "$0.0078") {
		t.Errorf("cost should show in the spend: %q", got)
	}
}

func TestStatusLineHidesCostWithoutPricing(t *testing.T) {
	m := statusModel()
	m.modelName = "m"
	m.provName = "p"
	m.agent.Model = "unpriced"
	m.catalogs = map[string]config.Catalog{
		"p": {Models: []config.ModelInfoLite{{ID: "unpriced"}}},
	}
	m.agent.AddUsage(ai.Usage{PromptTokens: 10000, CompletionTokens: 1000})

	if got := m.statusView(); strings.Contains(got, "$") {
		t.Errorf("unpriced model should hide cost: %q", got)
	}

	m.catalogs = nil
	if got := m.statusView(); strings.Contains(got, "$") {
		t.Errorf("missing catalog should hide cost: %q", got)
	}
}

func TestStatusLineShowsLastResponse(t *testing.T) {
	m := statusModel()
	m.modelName = "m"
	m.provName = "p"
	m.agent.AddUsage(ai.Usage{PromptTokens: 20000, CompletionTokens: 900})
	m.Update(usageMsg(ai.Usage{PromptTokens: 10000, CompletionTokens: 300}))

	got := m.statusView()
	if !strings.Contains(got, "last 10.0k/300 tok") {
		t.Errorf("last response tokens should show in the status: %q", got)
	}

	if !strings.Contains(got, "20.0k/900 tok") {
		t.Errorf("session spend segment should be unchanged: %q", got)
	}
}

func TestStatusLineShowsSessionTitleAtRight(t *testing.T) {
	m := statusModel()
	m.modelName = "model1"
	m.provName = "provider"
	m.sessTitle = "Investigate the build"

	got := m.statusView()
	if !strings.Contains(got, "Investigate the build") {
		t.Fatalf("session title should be shown in the bottom status line: %q", got)
	}
	if strings.LastIndex(got, "Investigate the build") < strings.LastIndex(got, "provider") {
		t.Fatalf("session title should be the rightmost status segment: %q", got)
	}
	plain := ansi.Strip(got)
	if !strings.HasSuffix(plain, "Investigate the build") {
		t.Fatalf("session title should be flush with the right edge: %q", plain)
	}
	if w := lipgloss.Width(plain); w != m.width {
		t.Fatalf("status line width %d should fill terminal width %d: %q", w, m.width, got)
	}
}

func TestStatusLineOmitsEmptySessionTitle(t *testing.T) {
	m := statusModel()
	m.modelName = "model1"
	m.provName = "provider"
	m.sessTitle = "   \n\t"

	got := m.statusView()
	withoutTitle := statusModel()
	withoutTitle.modelName = m.modelName
	withoutTitle.provName = m.provName
	if want := withoutTitle.statusView(); got != want {
		t.Fatalf("empty session title should not add a segment: got %q, want %q", got, want)
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
			lites[i].InPrice, lites[i].OutPrice, lites[i].CacheReadPrice = mi.Pricing.Rates()
		}
	}
	m := statusModel()
	m.provName = "inference"
	m.catalogs = map[string]config.Catalog{"inference": {Models: lites}}
	u := ai.Usage{PromptTokens: 31100, CompletionTokens: 360}
	u.PromptTokensDetails = &struct {
		CachedTokens int `json:"cached_tokens"`
	}{CachedTokens: 20700}
	m.agent.AddUsage(u)

	m.agent.Model = "kimi-k3-fast"
	fast, ok := m.sessionCost()
	if !ok {
		t.Fatal("fast variant should be priced")
	}
	m.agent.Model = "kimi-k3"
	std, ok := m.sessionCost()
	if !ok {
		t.Fatal("standard variant should be priced")
	}
	if fast <= std {
		t.Errorf("kimi-k3-fast cost %v should exceed kimi-k3 %v", fast, std)
	}

	if want := 0.064215; fast != want {
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

func TestStatusTitleSurvivesCrowdedFooter(t *testing.T) {
	for _, width := range []int{20, 40, 80, 120} {
		m := statusModel()
		m.width = width
		m.modelName = strings.Repeat("model", 20)
		m.provName = strings.Repeat("provider", 10)
		m.lastResp = ai.Usage{PromptTokens: 10000, CompletionTokens: 300}
		m.sessTitle = "修复构建"
		line := ansi.Strip(m.statusView())
		if !strings.HasSuffix(line, m.sessTitle) {
			t.Fatalf("width %d hides title: %q", width, line)
		}
		if got := lipgloss.Width(line); got != width {
			t.Fatalf("footer width %d, want %d", got, width)
		}
	}
}
