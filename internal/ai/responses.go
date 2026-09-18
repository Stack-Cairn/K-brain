package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

type Responses struct{ *OpenAI }

func NewResponses(baseURL, apiKey string) *Responses {
	return &Responses{OpenAI: New(baseURL, apiKey)}
}

func (c *Responses) Clone() Client { cp := *c.OpenAI; return &Responses{OpenAI: &cp} }

func (c *Responses) Stream(ctx context.Context, req Request, onText, onThink func(string), onToolCall func(id, name, args string)) (Message, Usage, error) {
	req.Messages = repairToolHistory(stripAuthored(req.Messages))
	c.applyCache(&req)
	payload := responsesPayload(req, true)
	body, err := json.Marshal(payload)
	if err != nil {
		return Message{}, Usage{}, err
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/responses", bytes.NewReader(body))
	if err != nil {
		return Message{}, Usage{}, err
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Authorization", "Bearer "+c.APIKey)
	c.applyCacheHeaders(hr)
	resp, err := c.HTTP.Do(hr)
	if err != nil {
		return Message{}, Usage{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return Message{}, Usage{}, newHTTPError(resp, string(b))
	}
	msg := Message{Role: "assistant"}
	var usage Usage
	calls := map[string]*ToolCall{}
	order := []string{}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" || data == "" {
			continue
		}
		var ev struct {
			Type     string          `json:"type"`
			Delta    string          `json:"delta"`
			Item     json.RawMessage `json:"item"`
			Response json.RawMessage `json:"response"`
			Usage    json.RawMessage `json:"usage"`
			ItemID   string          `json:"item_id"`
			CallID   string          `json:"call_id"`
			Name     string          `json:"name"`
		}
		if json.Unmarshal([]byte(data), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "response.output_text.delta":
			msg.Content += ev.Delta
			if onText != nil {
				onText(ev.Delta)
			}
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			if onThink != nil {
				onThink(ev.Delta)
			}
		case "response.output_item.added":
			var item struct {
				Type      string `json:"type"`
				ID        string `json:"id"`
				CallID    string `json:"call_id"`
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}
			if json.Unmarshal(ev.Item, &item) == nil && item.Type == "function_call" {
				id := item.CallID
				if id == "" {
					id = item.ID
				}
				calls[id] = &ToolCall{ID: id, Type: "function"}
				calls[id].Function.Name = item.Name
				order = append(order, id)
			}
		case "response.function_call_arguments.delta":
			id := ev.ItemID
			if id == "" {
				id = ev.CallID
			}
			tc := calls[id]
			if tc == nil {
				tc = &ToolCall{ID: id, Type: "function"}
				calls[id] = tc
				order = append(order, id)
			}
			tc.Function.Arguments += ev.Delta
			if onToolCall != nil {
				onToolCall(tc.ID, tc.Function.Name, tc.Function.Arguments)
			}
		case "response.output_item.done":
			var item struct {
				Type      string `json:"type"`
				ID        string `json:"id"`
				CallID    string `json:"call_id"`
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}
			if json.Unmarshal(ev.Item, &item) == nil && item.Type == "function_call" {
				id := item.CallID
				if id == "" {
					id = item.ID
				}
				tc := calls[id]
				if tc == nil {
					tc = &ToolCall{ID: id, Type: "function"}
					calls[id] = tc
					order = append(order, id)
				}
				if item.Name != "" {
					tc.Function.Name = item.Name
				}
				if item.Arguments != "" {
					tc.Function.Arguments = item.Arguments
				}
				if onToolCall != nil {
					onToolCall(tc.ID, tc.Function.Name, tc.Function.Arguments)
				}
			}
		case "response.completed":
			usage = responsesUsage(ev.Response, ev.Usage)
		}
	}
	if err := sc.Err(); err != nil {
		return Message{}, usage, err
	}
	for _, id := range order {
		if tc := calls[id]; tc != nil && validToolCallArgs(tc.Function.Arguments) {
			msg.ToolCalls = append(msg.ToolCalls, *tc)
		}
	}
	return msg, usage, nil
}

func (c *Responses) Complete(ctx context.Context, req Request) (string, Usage, error) {
	req.Messages = stripAuthored(req.Messages)
	c.applyCache(&req)
	body, err := json.Marshal(responsesPayload(req, false))
	if err != nil {
		return "", Usage{}, err
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/responses", bytes.NewReader(body))
	if err != nil {
		return "", Usage{}, err
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Authorization", "Bearer "+c.APIKey)
	c.applyCacheHeaders(hr)
	resp, err := c.HTTP.Do(hr)
	if err != nil {
		return "", Usage{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", Usage{}, newHTTPError(resp, string(b))
	}
	var wire struct {
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Usage json.RawMessage `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return "", Usage{}, err
	}
	var b strings.Builder
	for _, item := range wire.Output {
		for _, part := range item.Content {
			b.WriteString(part.Text)
		}
	}
	return b.String(), responsesUsage(nil, wire.Usage), nil
}

func responsesPayload(req Request, stream bool) map[string]any {
	p := map[string]any{"model": req.Model, "input": responsesInput(req.Messages), "stream": stream}
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
	return p
}

func responsesInput(msgs []Message) []any {
	out := make([]any, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case "tool":
			out = append(out, map[string]any{"type": "function_call_output", "call_id": m.ToolCallID, "output": m.Content})
		case "assistant":
			if m.Content != "" {
				out = append(out, map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": m.Content}}})
			}
			for _, tc := range m.ToolCalls {
				out = append(out, map[string]any{"type": "function_call", "call_id": tc.ID, "name": tc.Function.Name, "arguments": tc.Function.Arguments})
			}
		default:
			out = append(out, map[string]any{"type": "message", "role": m.Role, "content": []any{map[string]any{"type": "input_text", "text": m.Content}}})
		}
	}
	return out
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
			CachedTokens int `json:"cached_tokens"`
		} `json:"input_tokens_details"`
	}
	_ = json.Unmarshal(raw, &u)
	return Usage{PromptTokens: u.InputTokens, CompletionTokens: u.OutputTokens, PromptCacheHitTokens: u.InputDetails.CachedTokens}
}
