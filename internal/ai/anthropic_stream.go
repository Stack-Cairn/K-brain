package ai

import (
	"context"
	"encoding/json"
	"fmt"
)

type anthropicToolBlock struct {
	call     ToolCall
	initial  json.RawMessage
	streamed bool
	closed   bool
}

func (c *Anthropic) Stream(ctx context.Context, req Request, onText, onThink func(string), onToolCall func(id, name, args string)) (Message, Usage, error) {
	req.Stream = true
	resp, err := c.request(ctx, req, true)
	if err != nil {
		return Message{}, Usage{}, err
	}
	defer resp.Body.Close()
	msg := Message{Role: "assistant"}
	var usage anthropicUsage
	blocks := map[int]*anthropicToolBlock{}
	ids := map[string]bool{}
	var order []int
	stopReason := ""
	started := false
	sse := newSSEReader(resp.Body)
	for {
		event, ok := sse.next()
		if !ok {
			return Message{}, usage.normalized(), sse.endError("Anthropic Messages")
		}
		var v struct {
			Type         string          `json:"type"`
			Index        int             `json:"index"`
			Delta        json.RawMessage `json:"delta"`
			ContentBlock json.RawMessage `json:"content_block"`
			Usage        json.RawMessage `json:"usage"`
			Message      json.RawMessage `json:"message"`
			Error        json.RawMessage `json:"error"`
		}
		if err := decodeStreamEvent(event.Data, &v); err != nil {
			return Message{}, usage.normalized(), err
		}
		if v.Type == "" {
			v.Type = event.Type
		}
		if len(v.Error) > 0 && string(v.Error) != "null" {
			return Message{}, usage.normalized(), providerStreamError(v.Error, "Anthropic request failed")
		}
		switch v.Type {
		case "error":
			return Message{}, usage.normalized(), providerStreamError(json.RawMessage(event.Data), "Anthropic request failed")
		case "message_start":
			var start struct {
				Usage json.RawMessage `json:"usage"`
			}
			if err := json.Unmarshal(v.Message, &start); err != nil {
				return Message{}, usage.normalized(), err
			}
			if len(start.Usage) > 0 {
				if err := json.Unmarshal(start.Usage, &usage); err != nil {
					return Message{}, usage.normalized(), err
				}
			}
			started = true
		case "content_block_start":
			var b struct {
				Type, ID, Name, Text, Thinking string
				Input                          json.RawMessage
			}
			if err := json.Unmarshal(v.ContentBlock, &b); err != nil {
				return Message{}, usage.normalized(), err
			}
			if b.Text != "" {
				msg.Content += b.Text
				if onText != nil {
					onText(b.Text)
				}
			}
			if b.Thinking != "" && onThink != nil {
				onThink(b.Thinking)
			}
			if b.Type == "tool_use" {
				if b.ID == "" || b.Name == "" || blocks[v.Index] != nil || ids[b.ID] {
					return Message{}, usage.normalized(), providerStreamError(nil, "invalid tool block identity")
				}
				ids[b.ID] = true
				call := ToolCall{ID: b.ID, Type: "function"}
				call.Function.Name = b.Name
				blocks[v.Index] = &anthropicToolBlock{call: call, initial: b.Input}
				order = append(order, v.Index)
			}
		case "content_block_delta":
			var d struct {
				Type, Text, Thinking string
				PartialJSON          string `json:"partial_json"`
			}
			if err := json.Unmarshal(v.Delta, &d); err != nil {
				return Message{}, usage.normalized(), err
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
			if d.Type == "input_json_delta" {
				block := blocks[v.Index]
				if block == nil || block.closed {
					return Message{}, usage.normalized(), providerStreamError(nil, "tool delta has no open content block")
				}
				block.streamed = true
				block.call.Function.Arguments += d.PartialJSON
				if onToolCall != nil {
					onToolCall(block.call.ID, block.call.Function.Name, block.call.Function.Arguments)
				}
			}
		case "content_block_stop":
			if block := blocks[v.Index]; block != nil {
				block.closed = true
				if !block.streamed {
					block.call.Function.Arguments = string(block.initial)
				}
			}
		case "message_delta":
			if len(v.Usage) > 0 {
				if err := json.Unmarshal(v.Usage, &usage); err != nil {
					return Message{}, usage.normalized(), err
				}
			}
			if len(v.Delta) > 0 {
				var d struct {
					StopReason string `json:"stop_reason"`
				}
				if err := json.Unmarshal(v.Delta, &d); err != nil {
					return Message{}, usage.normalized(), err
				}
				if d.StopReason != "" {
					stopReason = d.StopReason
				}
			}
		case "message_stop":
			if !started {
				return Message{}, usage.normalized(), providerStreamError(nil, "message_stop without message_start")
			}
			if stopReason == "max_tokens" {
				return finishTruncatedMessage(msg, stopReason, len(order) > 0, onText), usage.normalized(), nil
			}
			for _, index := range order {
				block := blocks[index]
				if !block.closed {
					return Message{}, usage.normalized(), providerStreamError(nil, "message stopped with an unfinished tool block")
				}
				if !validToolCallArgs(block.call.Function.Arguments) {
					msg.Content += fmt.Sprintf("\n[tool call %q discarded: arguments are invalid JSON]", block.call.Function.Name)
					continue
				}
				msg.ToolCalls = append(msg.ToolCalls, block.call)
			}
			return finishMessage(msg, msg.ToolCalls, stopReason, onText), usage.normalized(), nil
		}
	}
}
