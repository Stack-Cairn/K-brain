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
	sctx, idleGuard := newIdleStream(ctx, idleOr(c.StreamIdle), "Anthropic")
	defer idleGuard.close()
	resp, err := c.request(sctx, req, true)
	if err != nil {
		return Message{}, Usage{}, err
	}
	defer resp.Body.Close()
	msg := Message{Role: "assistant"}
	search := newSearchStream(ctx, "claude_code")
	var usage anthropicUsage
	blocks := map[int]*anthropicToolBlock{}
	thinking := map[int]map[string]any{}
	var thinkingOrder []int
	serverBlocks := map[int]bool{}
	ids := map[string]bool{}
	var order []int
	stopReason := ""
	started := false
	sse := newSSEReader(idleGuard.wrap(resp.Body))
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
		search.accept(event.Data, event.Type)
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
			if b.Type == "thinking" || b.Type == "redacted_thinking" {
				var block map[string]any
				if err := json.Unmarshal(v.ContentBlock, &block); err != nil {
					return Message{}, usage.normalized(), err
				}
				thinking[v.Index] = block
				thinkingOrder = append(thinkingOrder, v.Index)
			}
			if b.Type == "server_tool_use" {
				serverBlocks[v.Index] = true
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
				Type, Text, Thinking, Signature string
				PartialJSON                     string `json:"partial_json"`
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
			if block := thinking[v.Index]; block != nil {
				if d.Type == "thinking_delta" {
					text, _ := block["thinking"].(string)
					block["thinking"] = text + d.Thinking
				}
				if d.Type == "signature_delta" {
					signature, _ := block["signature"].(string)
					block["signature"] = signature + d.Signature
				}
			}
			if d.Type == "input_json_delta" {
				if serverBlocks[v.Index] {
					continue
				}
				block := blocks[v.Index]
				if block == nil || block.closed {
					return Message{}, usage.normalized(), providerStreamError(nil, "tool delta has no open content block")
				}
				block.streamed = true
				block.call.Function.Arguments += d.PartialJSON
				if onToolCall != nil && !req.hostedSearchCall(block.call.Function.Name) {
					onToolCall(block.call.ID, block.call.Function.Name, block.call.Function.Arguments)
				}
			}
		case "content_block_stop":
			delete(serverBlocks, v.Index)
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
				if req.hostedSearchCall(block.call.Function.Name) {
					msg.ToolCalls = append(msg.ToolCalls, block.call)
					continue
				}
				if !block.closed {
					return Message{}, usage.normalized(), providerStreamError(nil, "message stopped with an unfinished tool block")
				}
				if !validToolCallArgs(block.call.Function.Arguments) {
					msg.Content += fmt.Sprintf("\n[tool call %q discarded: arguments are invalid JSON]", block.call.Function.Name)
					continue
				}
				msg.ToolCalls = append(msg.ToolCalls, block.call)
			}
			search.finish(false)
			if len(thinkingOrder) > 0 {
				msg.Replay = &ProviderReplay{API: APIMessages, Endpoint: c.Endpoint(), Model: req.Model}
				for _, index := range thinkingOrder {
					block := thinking[index]
					signature, _ := block["signature"].(string)
					if block["type"] == "thinking" && signature == "" {
						continue
					}
					raw, err := json.Marshal(block)
					if err != nil {
						return Message{}, usage.normalized(), err
					}
					msg.Replay.Blocks = append(msg.Replay.Blocks, raw)
				}
			}
			msg.HostedSearch = append([]HostedSearch(nil), search.blocks...)
			if req.NativeWebSearch {
				recoverNativeSearchCalls(req, &msg, onText)
			}
			return finishMessage(msg, msg.ToolCalls, stopReason, onText), usage.normalized(), nil
		}
	}
}
