package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func TestFmtTok(t *testing.T) {
	for in, want := range map[int]string{
		0: "0", 999: "999", 1000: "1.0k", 12345: "12.3k",
		1_000_000: "1.0M", 1_234_567: "1.2M",
	} {
		if got := fmtTok(in); got != want {
			t.Errorf("fmtTok(%d) = %q, want %q", in, got, want)
		}
	}
}

// footerRows returns the two info rows under the input box (the hints row and
// the status row), which is where the old top header's fields now live.
func footerRows(m *model) string {
	return ansi.Strip(m.footerHints() + "\n" + m.statusView())
}

func TestFooterShowsUsage(t *testing.T) {
	m := compactCmdModel()
	m.modelName = "kimi-k3-fast"
	m.agent = agent.New(ai.New("https://x", "k"), "kimi-k3-fast", 100, "sys")
	m.agent.ContextLimit = 100000
	m.follow = true
	m.agent.AddUsage(ai.Usage{PromptTokens: 12345, CompletionTokens: 678})
	m.agent.AddUsage(ai.Usage{
		PromptTokens: 1,
		PromptTokensDetails: &struct {
			CachedTokens int `json:"cached_tokens"`
		}{CachedTokens: 4000},
	})
	m.width = 200
	rows := footerRows(m)
	for _, want := range []string{"kimi-k3-fast", "✦ off", "12.3k(4.0k)/678 tok", "% ctx"} {
		if !strings.Contains(rows, want) {
			t.Errorf("footer missing %q: %q", want, rows)
		}
	}
}

func TestFooterOmitsUsageUntilReported(t *testing.T) {
	m := compactCmdModel()
	m.width = 120
	rows := footerRows(m)
	if !strings.Contains(rows, "0/0 tok") {
		t.Errorf("an unused session should read 0/0 tok: %q", rows)
	}
	if !strings.Contains(rows, "✦ off") || !strings.Contains(rows, m.modelName) {
		t.Errorf("model and effort always show: %q", rows)
	}
}
