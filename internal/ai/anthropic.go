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

type Anthropic struct{ *OpenAI }

func NewAnthropic(baseURL, apiKey string) *Anthropic { return &Anthropic{OpenAI: New(baseURL, apiKey)} }
func (c *Anthropic) Clone() Client                   { cp := *c.OpenAI; return &Anthropic{OpenAI: &cp} }

func (c *Anthropic) Models(ctx context.Context) ([]ModelInfo, error) {
	hr, err := http.NewRequestWithContext(ctx, http.MethodGet, modelCatalogURL(c.BaseURL), nil)
	if err != nil {
		return nil, err
	}
	hr.Header.Set("x-api-key", c.APIKey)
	hr.Header.Set("anthropic-version", "2023-06-01")
	resp, err := c.HTTP.Do(hr)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, newHTTPError(resp, string(b))
	}
	var list struct {
		Data []ModelInfo `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, err
	}
	return list.Data, nil
}

func (c *Anthropic) request(ctx context.Context, req Request, stream bool) (*http.Response, error) {
	req.Messages = repairToolHistory(stripAuthored(req.Messages))
	c.applyCache(&req)
	if c.CacheRetention != "none" && req.PromptCacheRetention == "" {
		req.PromptCacheRetention = "short"
	}
	body, err := json.Marshal(anthropicPayload(req, stream))
	if err != nil {
		return nil, err
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hr.Header.Set("content-type", "application/json")
	hr.Header.Set("x-api-key", c.APIKey)
	hr.Header.Set("authorization", "Bearer "+c.APIKey)
	hr.Header.Set("anthropic-version", "2023-06-01")
	if stream {
		hr.Header.Set("accept", "text/event-stream")
	}
	c.applyCacheHeaders(hr)
	resp, err := c.HTTP.Do(hr)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, newHTTPError(resp, string(b))
	}
	return resp, nil
}

func (c *Anthropic) Stream(ctx context.Context, req Request, onText, onThink func(string), onToolCall func(id, name, args string)) (Message, Usage, error) {
	req.Stream = true
	resp, err := c.request(ctx, req, true)
	if err != nil {
		return Message{}, Usage{}, err
	}
	defer resp.Body.Close()
	msg := Message{Role: "assistant"}
	var usage Usage
	var calls []ToolCall
	var current int = -1
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	event := ""
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		var v struct {
			Type         string          `json:"type"`
			Index        int             `json:"index"`
			Delta        json.RawMessage `json:"delta"`
			ContentBlock json.RawMessage `json:"content_block"`
			Usage        json.RawMessage `json:"usage"`
			Message      json.RawMessage `json:"message"`
		}
		if json.Unmarshal([]byte(data), &v) != nil {
			continue
		}
		typ := v.Type
		if typ == "" {
			typ = event
		}
		switch typ {
		case "message_start":
			var x struct {
				Message struct {
					Usage struct {
						InputTokens          int `json:"input_tokens"`
						CacheReadInputTokens int `json:"cache_read_input_tokens"`
						CacheCreationTokens  int `json:"cache_creation_input_tokens"`
					} `json:"usage"`
				} `json:"message"`
			}
			_ = json.Unmarshal([]byte(data), &x)
			usage.PromptTokens = x.Message.Usage.InputTokens
			usage.PromptCacheHitTokens = x.Message.Usage.CacheReadInputTokens
			usage.PromptCacheWriteTokens = x.Message.Usage.CacheCreationTokens
		case "content_block_start":
			var b struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
			}
			if json.Unmarshal(v.ContentBlock, &b) == nil && b.Type == "tool_use" {
				calls = append(calls, ToolCall{ID: b.ID, Type: "function"})
				calls[len(calls)-1].Function.Name = b.Name
				current = len(calls) - 1
			}
		case "content_block_delta":
			var d struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				Thinking    string `json:"thinking"`
				PartialJSON string `json:"partial_json"`
			}
			if json.Unmarshal(v.Delta, &d) != nil {
				continue
			}
			if d.Text != "" {
				msg.Content += d.Text
				if onText != nil {
					onText(d.Text)
				}
			}
			if d.Thinking != "" && onThink != nil {
				onThink(d.Thinking)
			}
			if d.PartialJSON != "" && current >= 0 {
				calls[current].Function.Arguments += d.PartialJSON
				if onToolCall != nil {
					onToolCall(calls[current].ID, calls[current].Function.Name, calls[current].Function.Arguments)
				}
			}
		case "message_delta":
			var x struct {
				Usage struct {
					OutputTokens         int `json:"output_tokens"`
					CacheReadInputTokens int `json:"cache_read_input_tokens"`
					CacheCreationTokens  int `json:"cache_creation_input_tokens"`
				} `json:"usage"`
			}
			_ = json.Unmarshal(v.Delta, &x)
			if x.Usage.OutputTokens > 0 {
				usage.CompletionTokens = x.Usage.OutputTokens
			}
			if x.Usage.CacheReadInputTokens > 0 {
				usage.PromptCacheHitTokens = x.Usage.CacheReadInputTokens
			}
			if x.Usage.CacheCreationTokens > 0 {
				usage.PromptCacheWriteTokens = x.Usage.CacheCreationTokens
			}
			if len(v.Usage) > 0 {
				_ = json.Unmarshal(v.Usage, &x.Usage)
				usage.CompletionTokens = x.Usage.OutputTokens
			}
		}
	}
	if err := sc.Err(); err != nil {
		return Message{}, usage, err
	}
	for _, tc := range calls {
		if validToolCallArgs(tc.Function.Arguments) {
			msg.ToolCalls = append(msg.ToolCalls, tc)
		}
	}
	return msg, usage, nil
}

func (c *Anthropic) Complete(ctx context.Context, req Request) (string, Usage, error) {
	resp, err := c.request(ctx, req, false)
	if err != nil {
		return "", Usage{}, err
	}
	defer resp.Body.Close()
	var w struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			InputTokens          int `json:"input_tokens"`
			OutputTokens         int `json:"output_tokens"`
			CacheReadInputTokens int `json:"cache_read_input_tokens"`
			CacheCreationTokens  int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&w); err != nil {
		return "", Usage{}, err
	}
	var b strings.Builder
	for _, p := range w.Content {
		if p.Type == "text" {
			b.WriteString(p.Text)
		}
	}
	return b.String(), Usage{PromptTokens: w.Usage.InputTokens, CompletionTokens: w.Usage.OutputTokens, PromptCacheHitTokens: w.Usage.CacheReadInputTokens, PromptCacheWriteTokens: w.Usage.CacheCreationTokens}, nil
}

func anthropicPayload(req Request, stream bool) map[string]any {
	p := map[string]any{"model": req.Model, "max_tokens": req.MaxTokens, "messages": anthropicMessages(req.Messages), "stream": stream}
	cache := req.PromptCacheRetention != "none" && req.PromptCacheRetention != ""
	for _, m := range req.Messages {
		if m.Role == "system" && m.Content != "" {
			if cache {
				p["system"] = []any{map[string]any{"type": "text", "text": m.Content, "cache_control": map[string]any{"type": "ephemeral"}}}
			} else {
				p["system"] = m.Content
			}
			break
		}
	}
	if req.MaxTokens <= 0 {
		p["max_tokens"] = 4096
	}
	if req.ReasoningEffort != "" {
		p["thinking"] = map[string]any{"type": "enabled", "budget_tokens": reasoningBudget(req.ReasoningEffort, req.MaxTokens)}
	}
	if cache {
		if ms, ok := p["messages"].([]any); ok && len(ms) > 0 {
			if last, ok := ms[len(ms)-1].(map[string]any); ok {
				if blocks, ok := last["content"].([]any); ok && len(blocks) > 0 {
					if b, ok := blocks[len(blocks)-1].(map[string]any); ok {
						b["cache_control"] = map[string]any{"type": "ephemeral"}
					}
				}
			}
		}
	}
	if len(req.Tools) > 0 {
		ts := make([]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			var schema any
			_ = json.Unmarshal(t.Function.Parameters, &schema)
			ts = append(ts, map[string]any{"name": t.Function.Name, "description": t.Function.Description, "input_schema": schema})
		}
		p["tools"] = ts
	}
	return p
}
func reasoningBudget(e string, max int) int {
	n := max * 2
	if n < 1024 {
		n = 1024
	}
	switch e {
	case "low":
		n = 1024
	case "medium":
		n = 4096
	case "high":
		n = 8192
	case "xhigh", "max":
		n = 16384
	}
	return n
}
func anthropicMessages(msgs []Message) []any {
	out := []any{}
	for _, m := range msgs {
		if m.Role == "system" {
			continue
		}
		role := m.Role
		if role == "tool" {
			out = append(out, map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": m.ToolCallID, "content": m.Content}}})
			continue
		}
		blocks := []any{}
		if m.Content != "" {
			blocks = append(blocks, map[string]any{"type": "text", "text": m.Content})
		}
		for _, tc := range m.ToolCalls {
			var in any
			_ = json.Unmarshal([]byte(tc.Function.Arguments), &in)
			blocks = append(blocks, map[string]any{"type": "tool_use", "id": tc.ID, "name": tc.Function.Name, "input": in})
		}
		if len(blocks) == 0 {
			blocks = []any{map[string]any{"type": "text", "text": ""}}
		}
		out = append(out, map[string]any{"role": role, "content": blocks})
	}
	return out
}
