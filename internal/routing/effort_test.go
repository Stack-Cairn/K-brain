package routing

import (
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

func TestDefaultEffortForModelAware(t *testing.T) {
	cats := map[string]config.Catalog{
		"inference": {Models: []config.ModelInfoLite{
			{ID: "deepseek-v4-flash", ReasoningEfforts: []string{"low", "high", "max"}},
			{ID: "claude-opus-5", ReasoningEfforts: []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}},
			{ID: "gemini-3.5-flash"},
		}},
	}
	cases := []struct{ model, pinned, want string }{
		{"deepseek-v4-flash", "", "low"},
		{"claude-opus-5", "", "low"},
		{"gemini-3.5-flash", "", ""},
		{"deepseek-v4-flash", "high", "high"},
		{"deepseek-v4-flash", "medium", "medium"},
	}
	for _, c := range cases {
		if got := DefaultEffortFor(cats, "inference", c.model, c.pinned); got != c.want {
			t.Fatalf("DefaultEffortFor(%q, pinned=%q): got %q want %q", c.model, c.pinned, got, c.want)
		}
	}

	if got := DefaultEffortFor(map[string]config.Catalog{}, "elsewhere", "anything", ""); got != "low" {
		t.Fatalf("unknown provider should fall back to low, got %q", got)
	}
}
