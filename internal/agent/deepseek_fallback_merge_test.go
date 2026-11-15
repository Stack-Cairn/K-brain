package agent

import (
	"fmt"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func TestDeepSeekFallbackPreservesModelNativeSearch(t *testing.T) {
	for _, model := range []string{"deepseek-flash", "DEEPSEEK-FLASH", "deepseek-pro", "deepseek-chat", "fixture-model"} {
		for _, enabled := range []bool{true, false} {
			for _, configured := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s/enabled=%t/configured=%t", model, enabled, configured), func(t *testing.T) {
					a := New(&ai.OpenAI{}, model, 100, "sys")
					a.NativeWebSearch = enabled
					ran := 0
					a.WebSearchFallback = func() (tools.Tool, bool) { return fakeSearchTool(&ran), configured }
					native := model == "deepseek-flash" || model == "DEEPSEEK-FLASH"
					wantFallback := enabled && configured && !native
					if got := offersTool(a.AllTools(), tools.WebSearchToolName); got != wantFallback {
						t.Fatalf("web_search offered = %t, want %t", got, wantFallback)
					}
					if got := a.requestNativeSearch(); got != (enabled && !wantFallback) {
						t.Fatalf("native search = %t, enabled=%t fallback=%t", got, enabled, wantFallback)
					}
				})
			}
		}
	}
}
