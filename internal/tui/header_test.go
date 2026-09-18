package tui

import (
	"strings"
	"testing"

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

func TestHeaderShowsUsage(t *testing.T) {
	m := compactCmdModel()
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
	head, _, _ := strings.Cut(m.View(), "\n")
	for _, want := range []string{"kimi-k3-fast", "✦ off", "12.3k in", "4.0k cached", "678 out", "% ctx"} {
		if !strings.Contains(head, want) {
			t.Errorf("header missing %q: %q", want, head)
		}
	}
}

func TestHeaderOmitsUsageUntilReported(t *testing.T) {
	m := compactCmdModel()
	m.width = 120
	head, _, _ := strings.Cut(m.View(), "\n")
	if strings.Contains(head, "⣿") {
		t.Errorf("no usage should mean no token block: %q", head)
	}
	if !strings.Contains(head, "✦ off") || !strings.Contains(head, "kimi-k3-fast") {
		t.Errorf("model and effort always show: %q", head)
	}
}
