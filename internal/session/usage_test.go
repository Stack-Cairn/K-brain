package session

import (
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func TestUsageSummaryReconstructsMessageRoutes(t *testing.T) {
	u := ai.Usage{PromptTokens: 100, CompletionTokens: 10, PromptCacheHitTokens: 60, PromptCacheWriteTokens: 20}
	meta := Meta{Model: "current", Provider: "provider"}
	msgs := []ai.Message{
		{Role: "assistant", Model: "previous @ original", Usage: &u},
		{Role: "assistant", Usage: &u},
	}
	summary := meta.UsageSummary(msgs)
	if summary.Total.PromptTokens != 200 || summary.Total.Cached() != 120 || summary.Total.CacheWrite() != 40 || summary.Models["previous @ original"].PromptTokens != 100 || summary.Models["current @ provider"].PromptTokens != 100 {
		t.Fatalf("message usage = %+v", summary)
	}
	meta.setUsage(summary)
	if got := meta.UsageSummary(msgs); got.Total.PromptTokens != 200 || got.Total.CacheWrite() != 40 {
		t.Fatalf("metadata usage counted twice: %+v", got)
	}
}
