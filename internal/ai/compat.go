package ai

import (
	"encoding/json"
	"fmt"
	"strings"
)

func replayMatches(m Message, api, endpoint, model string) bool {
	return m.Role == "assistant" && m.Replay != nil && m.Replay.API == api && m.Replay.Endpoint == endpoint && m.Replay.Model == model
}

func reasoningDisabled(effort string) bool { return effort == "off" || effort == "none" }

func openAIReasoningModel(model string) bool {
	m := strings.ToLower(model)
	return strings.HasPrefix(m, "gpt-5") || strings.HasPrefix(m, "o1") || strings.HasPrefix(m, "o3") || strings.HasPrefix(m, "o4")
}

func openAIEffort(model, effort string) string {
	m := strings.ToLower(model)
	if reasoningDisabled(effort) {
		if strings.HasPrefix(m, "o1") || strings.HasPrefix(m, "o3") || strings.HasPrefix(m, "o4") || strings.Contains(m, "codex") {
			return "low"
		}
		if m == "gpt-5" || strings.HasPrefix(m, "gpt-5-") {
			return "minimal"
		}
		return "none"
	}
	if effort == "max" {
		effort = "xhigh"
	}
	if effort == "xhigh" && (strings.HasPrefix(m, "o") || m == "gpt-5" || strings.HasPrefix(m, "gpt-5-") || strings.HasPrefix(m, "gpt-5.1")) {
		return "high"
	}
	if effort == "minimal" && (strings.HasPrefix(m, "o") || strings.HasPrefix(m, "gpt-5.")) {
		return "low"
	}
	return effort
}

// Model identity works through custom relays as well as official endpoints.
func chatThinkingFormat(model string) string {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "deepseek"), strings.Contains(m, "glm-"), strings.Contains(m, "kimi"):
		return "thinking"
	case strings.Contains(m, "qwen"), strings.Contains(m, "qwq"):
		return "enable_thinking"
	case strings.Contains(m, "grok"):
		return "grok"
	default:
		return "reasoning_effort"
	}
}

func (c *OpenAI) chatBody(req Request) ([]byte, error) {
	if req.NativeWebSearch && !deepSeekWebToolsModel(req.Model) {
		return nil, fmt.Errorf("native web search is unsupported for Chat Completions model %q; use DeepSeek Flash, Responses, Anthropic Messages, or Gemini", req.Model)
	}
	// chatMessages strips private replay metadata before JSON serialization.
	history := req.Messages
	messages, err := chatMessages(history)
	if err != nil {
		return nil, err
	}
	req.Messages = messages
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	var p map[string]any
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if openAIReasoningModel(req.Model) {
		if value, ok := p["max_tokens"]; ok {
			p["max_completion_tokens"] = value
			delete(p, "max_tokens")
		}
		delete(p, "temperature")
		delete(p, "top_p")
	}
	effort := req.ReasoningEffort
	if effort != "" {
		switch chatThinkingFormat(req.Model) {
		case "thinking":
			kind := "enabled"
			if reasoningDisabled(effort) {
				kind = "disabled"
			}
			p["thinking"] = map[string]any{"type": kind}
			delete(p, "reasoning_effort")
		case "enable_thinking":
			p["enable_thinking"] = !reasoningDisabled(effort)
			delete(p, "reasoning_effort")
		case "grok":
			delete(p, "reasoning_effort")
		default:
			p["reasoning_effort"] = openAIEffort(req.Model, effort)
		}
	}
	// Match by the assistant row order; tool image conversion only inserts user rows.
	var replay []string
	for _, m := range history {
		if m.Role != "assistant" {
			continue
		}
		text := ""
		if replayMatches(m, APIChatCompletions, c.Endpoint(), req.Model) {
			for _, raw := range m.Replay.Blocks {
				var block struct {
					Text string `json:"text"`
				}
				if json.Unmarshal(raw, &block) == nil {
					text += block.Text
				}
			}
		}
		replay = append(replay, text)
	}
	i := 0
	for _, value := range p["messages"].([]any) {
		m := value.(map[string]any)
		if m["role"] == "assistant" {
			if replay[i] != "" {
				m["reasoning_content"] = replay[i]
			}
			i++
		}
	}
	if deepSeekFunctionSearch(req) {
		tools := append([]Tool(nil), req.Tools...)
		tools = append(tools, deepSeekSearchFunctions()...)
		p["tools"] = tools
	} else if req.NativeWebSearch {
		p["tools"] = appendSearchTool(p["tools"], map[string]any{"type": "deepseek_web_search"})
		p["tools"] = appendSearchTool(p["tools"], map[string]any{"type": "deepseek_read_url"})
	}
	applyDeepSeekChatReplay(p, history, c.Endpoint(), req.Model)
	c.applyChatCacheControl(p)
	return json.Marshal(p)
}
