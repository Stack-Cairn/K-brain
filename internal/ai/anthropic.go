package ai

import (
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
	payload, err := anthropicPayload(req, stream)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return c.postJSON(ctx, "/messages", body, stream, http.Header{
		"X-Api-Key":         {c.APIKey},
		"Anthropic-Version": {"2023-06-01"},
	})
}

func (c *Anthropic) Complete(ctx context.Context, req Request) (string, Usage, error) {
	resp, err := c.request(ctx, req, false)
	if err != nil {
		return "", Usage{}, err
	}
	defer resp.Body.Close()
	var w struct {
		StopReason string          `json:"stop_reason"`
		Error      json.RawMessage `json:"error"`
		Content    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage anthropicUsage `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&w); err != nil {
		return "", Usage{}, err
	}
	if len(w.Error) > 0 && string(w.Error) != "null" {
		return "", w.Usage.normalized(), providerStreamError(w.Error, "request failed")
	}
	var b strings.Builder
	for _, p := range w.Content {
		if p.Type == "text" {
			b.WriteString(p.Text)
		}
	}
	if w.StopReason == "max_tokens" {
		return b.String(), w.Usage.normalized(), &OutputLimitError{Reason: w.StopReason}
	}
	return b.String(), w.Usage.normalized(), nil
}

func anthropicPayload(req Request, stream bool) (map[string]any, error) {
	messages, err := anthropicMessages(req.Messages)
	if err != nil {
		return nil, err
	}
	p := map[string]any{"model": req.Model, "max_tokens": req.MaxTokens, "messages": messages, "stream": stream}
	cache := req.PromptCacheRetention != "none" && req.PromptCacheRetention != ""
	var system []any
	for _, m := range req.Messages {
		if m.Role == "system" || m.Role == "developer" {
			blocks, err := anthropicContent(m)
			if err != nil {
				return nil, err
			}
			system = append(system, blocks...)
		}
	}
	if len(system) > 0 {
		if cache {
			system[len(system)-1].(map[string]any)["cache_control"] = map[string]any{"type": "ephemeral"}
		}
		p["system"] = system
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
	return p, nil
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
func anthropicMessages(msgs []Message) ([]any, error) {
	out := []any{}
	var results []any
	flush := func() {
		if len(results) > 0 {
			out = append(out, map[string]any{"role": "user", "content": results})
			results = nil
		}
	}
	for _, m := range msgs {
		if m.Role == "system" || m.Role == "developer" {
			continue
		}
		role := m.Role
		blocks, err := anthropicContent(m)
		if err != nil {
			return nil, err
		}
		if role == "tool" {
			var content any = m.Content
			if len(m.Parts) > 0 {
				content = blocks
			}
			results = append(results, map[string]any{"type": "tool_result", "tool_use_id": m.ToolCallID, "content": content})
			continue
		}
		flush()
		for _, tc := range m.ToolCalls {
			in := map[string]any{}
			if tc.Function.Arguments != "" {
				if err := json.Unmarshal([]byte(tc.Function.Arguments), &in); err != nil {
					return nil, err
				}
			}
			blocks = append(blocks, map[string]any{"type": "tool_use", "id": tc.ID, "name": tc.Function.Name, "input": in})
		}
		if len(blocks) == 0 {
			continue
		}
		out = append(out, map[string]any{"role": role, "content": blocks})
	}
	flush()
	return out, nil
}
