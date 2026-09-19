package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

type Responses struct{ *OpenAI }

func NewResponses(baseURL, apiKey string) *Responses {
	return &Responses{OpenAI: New(baseURL, apiKey)}
}

func (c *Responses) Clone() Client { cp := *c.OpenAI; return &Responses{OpenAI: &cp} }

func (c *Responses) Complete(ctx context.Context, req Request) (string, Usage, error) {
	resp, err := c.request(ctx, req, false)
	if err != nil {
		return "", Usage{}, err
	}
	defer resp.Body.Close()
	var wire responsesResult
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return "", Usage{}, err
	}
	usage := responsesUsage(nil, wire.Usage)
	if err := wire.resultError(); err != nil {
		return "", usage, err
	}
	var b strings.Builder
	for _, item := range wire.Output {
		for _, part := range item.Content {
			b.WriteString(part.Text)
		}
	}
	if wire.Status == "incomplete" {
		return b.String(), usage, &OutputLimitError{Reason: wire.stopReason()}
	}
	return b.String(), usage, nil
}

func (c *Responses) request(ctx context.Context, req Request, stream bool) (*http.Response, error) {
	req.Messages = repairToolHistory(stripAuthored(req.Messages))
	c.applyCache(&req)
	payload, err := responsesPayload(req, stream)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return c.postJSON(ctx, "/responses", body, stream, nil)
}

func responsesPayload(req Request, stream bool) (map[string]any, error) {
	input, err := responsesInput(req.Messages)
	if err != nil {
		return nil, err
	}
	p := map[string]any{"model": req.Model, "input": input, "stream": stream}
	if req.MaxTokens > 0 {
		p["max_output_tokens"] = req.MaxTokens
	}
	if req.ReasoningEffort != "" {
		p["reasoning"] = map[string]any{"effort": req.ReasoningEffort}
	}
	if len(req.Tools) > 0 {
		tools := make([]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			var params any
			_ = json.Unmarshal(t.Function.Parameters, &params)
			tools = append(tools, map[string]any{"type": "function", "name": t.Function.Name, "description": t.Function.Description, "parameters": params})
		}
		p["tools"] = tools
	}
	if req.PromptCacheKey != "" {
		p["prompt_cache_key"] = req.PromptCacheKey
	}
	if req.PromptCacheRetention != "" {
		p["prompt_cache_retention"] = req.PromptCacheRetention
	}
	return p, nil
}

func responsesInput(msgs []Message) ([]any, error) {
	out := make([]any, 0, len(msgs))
	for _, m := range msgs {
		blocks, err := responsesContent(m)
		if err != nil {
			return nil, err
		}
		switch m.Role {
		case "tool":
			var output any = m.Content
			if len(m.Parts) > 0 {
				output = blocks
			}
			out = append(out, map[string]any{"type": "function_call_output", "call_id": m.ToolCallID, "output": output})
		case "assistant":
			if len(blocks) > 0 {
				out = append(out, map[string]any{"type": "message", "role": "assistant", "content": blocks})
			}
			for _, tc := range m.ToolCalls {
				out = append(out, map[string]any{"type": "function_call", "call_id": tc.ID, "name": tc.Function.Name, "arguments": tc.Function.Arguments})
			}
		default:
			if len(blocks) > 0 {
				out = append(out, map[string]any{"type": "message", "role": m.Role, "content": blocks})
			}
		}
	}
	return out, nil
}

func responsesUsage(response, direct json.RawMessage) Usage {
	raw := direct
	if len(raw) == 0 && len(response) > 0 {
		var x struct {
			Usage json.RawMessage `json:"usage"`
		}
		_ = json.Unmarshal(response, &x)
		raw = x.Usage
	}
	var u struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
		InputDetails struct {
			CachedTokens     int `json:"cached_tokens"`
			CacheWriteTokens int `json:"cache_write_tokens"`
		} `json:"input_tokens_details"`
	}
	_ = json.Unmarshal(raw, &u)
	return Usage{PromptTokens: u.InputTokens, CompletionTokens: u.OutputTokens, PromptCacheHitTokens: u.InputDetails.CachedTokens, PromptCacheWriteTokens: u.InputDetails.CacheWriteTokens}
}
