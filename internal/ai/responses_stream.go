package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type responsesItem struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Content   []struct {
		Text string `json:"text"`
	} `json:"content"`
}

type responsesResult struct {
	Status            string          `json:"status"`
	Output            []responsesItem `json:"output"`
	Usage             json.RawMessage `json:"usage"`
	Error             json.RawMessage `json:"error"`
	IncompleteDetails struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
}

func (r responsesResult) resultError() error {
	if len(r.Error) > 0 && string(r.Error) != "null" {
		return providerStreamError(r.Error, "Responses request failed")
	}
	switch r.Status {
	case "", "completed":
		return nil
	case "incomplete":
		if r.IncompleteDetails.Reason == "max_output_tokens" {
			return nil
		}
	}
	detail := "Responses request " + r.Status
	if r.IncompleteDetails.Reason != "" {
		detail += ": " + r.IncompleteDetails.Reason
	}
	return providerStreamError(nil, detail)
}

func (r responsesResult) stopReason() string {
	if r.Status == "incomplete" {
		return r.Status + "." + r.IncompleteDetails.Reason
	}
	return r.Status
}

type responsesCalls struct {
	items map[string]*ToolCall
	calls map[string]*ToolCall
	order []*ToolCall
}

func (c *responsesCalls) set(item responsesItem) (*ToolCall, error) {
	if item.CallID == "" || item.Name == "" {
		return nil, providerStreamError(nil, "invalid Responses tool identity")
	}
	tc := c.calls[item.CallID]
	if old := c.items[item.ID]; item.ID != "" && old != nil && old != tc {
		return nil, providerStreamError(nil, "conflicting Responses tool identity")
	}
	if tc == nil {
		tc = &ToolCall{ID: item.CallID, Type: "function"}
		c.calls[item.CallID] = tc
		c.order = append(c.order, tc)
	} else if tc.Function.Name != item.Name {
		return nil, providerStreamError(nil, "conflicting Responses tool name")
	}
	if item.ID != "" {
		c.items[item.ID] = tc
	}
	tc.Function.Name = item.Name
	tc.Function.Arguments = item.Arguments
	return tc, nil
}

func (c *Responses) Stream(ctx context.Context, req Request, onText, onThink func(string), onToolCall func(id, name, args string)) (Message, Usage, error) {
	resp, err := c.request(ctx, req, true)
	if err != nil {
		return Message{}, Usage{}, err
	}
	defer resp.Body.Close()
	msg := Message{Role: "assistant"}
	var usage Usage
	calls := responsesCalls{items: map[string]*ToolCall{}, calls: map[string]*ToolCall{}}
	notify := func(tc *ToolCall) {
		if onToolCall != nil {
			onToolCall(tc.ID, tc.Function.Name, tc.Function.Arguments)
		}
	}
	sse := newSSEReader(resp.Body)
	for {
		event, ok := sse.next()
		if !ok {
			return Message{}, usage, sse.endError("Responses")
		}
		var ev struct {
			Type      string          `json:"type"`
			Delta     string          `json:"delta"`
			Item      responsesItem   `json:"item"`
			Response  responsesResult `json:"response"`
			Usage     json.RawMessage `json:"usage"`
			Error     json.RawMessage `json:"error"`
			ItemID    string          `json:"item_id"`
			CallID    string          `json:"call_id"`
			Arguments string          `json:"arguments"`
		}
		if err := decodeStreamEvent(event.Data, &ev); err != nil {
			return Message{}, usage, err
		}
		if ev.Type == "" {
			ev.Type = event.Type
		}
		if len(ev.Response.Usage) > 0 && string(ev.Response.Usage) != "null" {
			usage = responsesUsage(nil, ev.Response.Usage)
		}
		if len(ev.Usage) > 0 && string(ev.Usage) != "null" {
			usage = responsesUsage(nil, ev.Usage)
		}
		if len(ev.Error) > 0 && string(ev.Error) != "null" {
			return Message{}, usage, providerStreamError(ev.Error, "Responses request failed")
		}
		switch ev.Type {
		case "error":
			return Message{}, usage, providerStreamError(json.RawMessage(event.Data), "Responses request failed")
		case "response.failed", "response.cancelled":
			if ev.Response.Status == "" {
				ev.Response.Status = ev.Type[len("response."):]
			}
			if err := ev.Response.resultError(); err != nil {
				return Message{}, usage, err
			}
			return Message{}, usage, providerStreamError(nil, ev.Type)
		case "response.output_text.delta":
			msg.Content += ev.Delta
			if onText != nil {
				onText(ev.Delta)
			}
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			if onThink != nil {
				onThink(ev.Delta)
			}
		case "response.output_item.added", "response.output_item.done":
			if ev.Item.Type == "function_call" {
				tc, err := calls.set(ev.Item)
				if err != nil {
					return Message{}, usage, err
				}
				notify(tc)
			}
		case "response.function_call_arguments.delta", "response.function_call_arguments.done":
			tc := calls.items[ev.ItemID]
			if tc == nil {
				tc = calls.calls[ev.CallID]
			}
			if tc == nil {
				return Message{}, usage, providerStreamError(nil, "Responses tool arguments have no output item")
			}
			if ev.CallID != "" && ev.CallID != tc.ID {
				return Message{}, usage, providerStreamError(nil, "conflicting Responses tool delta identity")
			}
			if ev.Type == "response.function_call_arguments.done" {
				tc.Function.Arguments = ev.Arguments
			} else {
				tc.Function.Arguments += ev.Delta
			}
			notify(tc)
		case "response.completed", "response.incomplete":
			if ev.Response.Status == "" {
				ev.Response.Status = strings.TrimPrefix(ev.Type, "response.")
			}
			if ev.Response.Status != strings.TrimPrefix(ev.Type, "response.") {
				return Message{}, usage, providerStreamError(nil, "conflicting Responses terminal status")
			}
			if err := ev.Response.resultError(); err != nil {
				return Message{}, usage, err
			}
			truncated := ev.Response.Status == "incomplete"
			hadTools := len(calls.order) > 0
			var finalText string
			for _, item := range ev.Response.Output {
				if item.Type == "function_call" {
					if truncated {
						hadTools = true
						continue
					}
					tc, err := calls.set(item)
					if err != nil {
						return Message{}, usage, err
					}
					notify(tc)
				}
				for _, part := range item.Content {
					finalText += part.Text
				}
			}
			if finalText != "" && strings.HasPrefix(finalText, msg.Content) {
				delta := strings.TrimPrefix(finalText, msg.Content)
				msg.Content = finalText
				if onText != nil && delta != "" {
					onText(delta)
				}
			}
			if truncated {
				return finishTruncatedMessage(msg, ev.Response.stopReason(), hadTools, onText), usage, nil
			}
			for _, tc := range calls.order {
				if !validToolCallArgs(tc.Function.Arguments) {
					msg.Content += fmt.Sprintf("\n[tool call %q discarded: arguments are invalid JSON]", tc.Function.Name)
					continue
				}
				msg.ToolCalls = append(msg.ToolCalls, *tc)
			}
			return finishMessage(msg, msg.ToolCalls, ev.Response.stopReason(), onText), usage, nil
		}
	}
}
