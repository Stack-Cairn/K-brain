package protocol

import (
	"encoding/json"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func TestHistoryMessageCarriesAuthoredProvenance(t *testing.T) {
	const text = "[subagent sub-1 cancelled] Review history\n\ncontext canceled"
	for _, authored := range []bool{true, false} {
		raw, err := json.Marshal(FromAIMessage(ai.Message{Role: "user", Content: text, Authored: authored}))
		if err != nil {
			t.Fatal(err)
		}
		var wire map[string]any
		if err := json.Unmarshal(raw, &wire); err != nil {
			t.Fatal(err)
		}
		if got := wire["authored"] == true; got != authored {
			t.Errorf("authored provenance = %v, want %v: %s", got, authored, raw)
		}
	}
}
