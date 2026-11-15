package ai

import (
	"encoding/json"
	"strings"
)

// Web tools currently have a documented contract only on DeepSeek Flash.
// Model identity also permits explicitly configured compatible relays.
func deepSeekWebToolsModel(model string) bool {
	return strings.EqualFold(model, "deepseek-flash")
}

// DeepSeek places progress beside delta, not in client function tool_calls.
func (s *searchStream) acceptDeepSeekChat(data string) json.RawMessage {
	var raw map[string]any
	if json.Unmarshal([]byte(data), &raw) != nil {
		return nil
	}
	choices := searchArray(raw["choices"])
	if len(choices) == 0 {
		return nil
	}
	choice := searchRecord(choices[0])
	event := searchRecord(choice["server_tool_event"])
	if typ := searchString(event["type"]); typ == "call_started" || typ == "call_completed" {
		s.deepSeekCall(searchRecord(event["call"]), typ == "call_started")
	}
	delta := searchRecord(choice["delta"])
	for _, step := range searchArray(delta["server_tool_calls"]) {
		for _, call := range searchArray(searchRecord(step)["calls"]) {
			s.deepSeekCall(searchRecord(call), false)
		}
	}
	for _, annotation := range searchArray(delta["annotations"]) {
		a := searchRecord(annotation)
		if searchString(a["type"]) == "url_citation" {
			for _, source := range searchSources(a["url_citation"], "citation") {
				id := ""
				for _, block := range s.blocks {
					for _, existing := range block.Sources {
						if existing.URL == source.URL {
							id = block.ID
							break
						}
					}
					if id != "" {
						break
					}
				}
				s.update(id, "completed", nil, []SearchSource{source}, "")
			}
		}
	}
	if calls, ok := delta["server_tool_calls"]; ok {
		encoded, _ := json.Marshal(calls)
		return encoded
	}
	return nil
}

func (s *searchStream) deepSeekCall(call map[string]any, started bool) {
	if call == nil {
		return
	}
	typ := searchString(call["type"])
	if typ != "deepseek_web_search" && typ != "deepseek_read_url" {
		return
	}
	action := searchRecord(call["action"])
	status := searchString(call["status"])
	if started {
		status = "searching"
	} else if status != "completed" && status != "failed" {
		status = "failed"
	}
	sources := searchSources(call["sources"], "source")
	sources = append(sources, searchSources(call["page"], "source")...)
	s.update(searchString(call["id"]), status, searchQueries(action["queries"]), sources, searchString(searchRecord(call["error"])["code"]))
}

// Reuse provider-private persisted replay, scoped by API, endpoint and model.
// In particular, read_url signatures must never cross routes after switching.
func applyDeepSeekChatReplay(payload map[string]any, history []Message, endpoint, model string) {
	var assistants []Message
	for _, m := range history {
		if m.Role == "assistant" {
			assistants = append(assistants, m)
		}
	}
	i := 0
	var expanded []any
	for _, value := range searchArray(payload["messages"]) {
		row := searchRecord(value)
		if row["role"] == "assistant" {
			m := assistants[i]
			i++
			if replayMatches(m, APIChatCompletions, endpoint, model) {
				for _, raw := range m.Replay.Blocks {
					var block map[string]json.RawMessage
					if json.Unmarshal(raw, &block) != nil {
						continue
					}
					if calls := block["server_tool_calls"]; len(calls) > 0 {
						row["server_tool_calls"] = calls
					}
					var turns []any
					if json.Unmarshal(block["deepseek_search_history"], &turns) == nil {
						expanded = append(expanded, turns...)
					}
				}
			}
		}
		expanded = append(expanded, value)
	}
	payload["messages"] = expanded
}
