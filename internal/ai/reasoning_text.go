package ai

import (
	"encoding/json"
	"strings"
)

// thoughtText keeps the reasoning a stream showed through onThink so the
// finished Message carries it as Reasoning. Reasoning is display-only: request
// history drops it (see stripAuthored), so keeping it never changes what is sent
// to a provider.
type thoughtText struct {
	b    strings.Builder
	part string
}

// wrap returns an onThink callback that records each delta before passing it on.
func (t *thoughtText) wrap(next func(string)) func(string) {
	return func(delta string) {
		t.b.WriteString(delta)
		if next != nil {
			next(delta)
		}
	}
}

// startPart separates reasoning parts that arrive one after another, such as
// the summary parts of a Responses reasoning item, so they do not run into
// each other. The separator is sent through think as well, which keeps the
// live text and the stored text the same.
func (t *thoughtText) startPart(key string, think func(string)) {
	if key == t.part {
		return
	}
	t.part = key
	if t.b.Len() > 0 && think != nil {
		think("\n\n")
	}
}

func (t *thoughtText) String() string {
	return t.b.String()
}

// responsesReasoningText is the readable text of stored Responses reasoning
// items: their summary parts, or their reasoning_text content when there is
// no summary. Encrypted-only items have none.
func responsesReasoningText(blocks []json.RawMessage) string {
	var parts []string
	for _, raw := range blocks {
		var item struct {
			Type    string `json:"type"`
			Summary []struct {
				Text string `json:"text"`
			} `json:"summary"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal(raw, &item) != nil || item.Type != "reasoning" {
			continue
		}
		found := false
		for _, s := range item.Summary {
			if strings.TrimSpace(s.Text) != "" {
				parts = append(parts, s.Text)
				found = true
			}
		}
		if found {
			continue
		}
		for _, c := range item.Content {
			if c.Type == "reasoning_text" && strings.TrimSpace(c.Text) != "" {
				parts = append(parts, c.Text)
			}
		}
	}
	return strings.Join(parts, "\n\n")
}
