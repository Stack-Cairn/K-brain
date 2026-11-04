package ai

import "strings"

type StopReason string

const (
	StopReasonStop      StopReason = "stop"
	StopReasonLength    StopReason = "length"
	StopReasonToolUse   StopReason = "tool_use"
	StopReasonOther     StopReason = "other"
	StopReasonError     StopReason = "error"
	StopReasonCancelled StopReason = "cancelled"
)

type OutputLimitError struct {
	Reason string
}

func (e *OutputLimitError) Error() string {
	return "response truncated by output limit: " + e.Reason
}

func finishMessage(msg Message, calls []ToolCall, raw string, onText func(string)) Message {
	msg.RawStopReason = raw
	switch raw {
	case "length", "max_tokens", "incomplete.max_output_tokens":
		return finishTruncatedMessage(msg, raw, len(calls) > 0, onText)
	case "", "stop", "end_turn", "stop_sequence", "completed":
		msg.StopReason = StopReasonStop
	case "tool_calls", "function_call", "tool_use":
		msg.StopReason = StopReasonToolUse
	default:
		msg.StopReason = StopReasonOther
	}
	msg.ToolCalls = calls
	if len(calls) > 0 && msg.StopReason == StopReasonStop {
		msg.StopReason = StopReasonToolUse
	}
	return msg
}

const (
	truncatedNote      = "\n[response truncated by output limit"
	discardedToolsNote = "; tool calls discarded]"
)

// TruncatedToolCalls reports whether msg was cut off by the output limit while it
// was still emitting tool calls. Those calls were dropped unexecuted.
func TruncatedToolCalls(msg Message) bool {
	return msg.StopReason == StopReasonLength && strings.HasSuffix(msg.Content, truncatedNote+discardedToolsNote)
}

func finishTruncatedMessage(msg Message, raw string, hadTools bool, onText func(string)) Message {
	msg.StopReason, msg.RawStopReason = StopReasonLength, raw
	msg.ToolCalls = nil
	note := truncatedNote + "]"
	if hadTools {
		note = truncatedNote + discardedToolsNote
	}
	msg.Content += note
	if onText != nil {
		onText(note)
	}
	return msg
}
