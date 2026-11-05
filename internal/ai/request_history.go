package ai

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
)

// prepareRequestHistory is shared by every wire adapter. Canonical stored history
// remains untouched; provider-private replay is used only on its originating route.
func prepareRequestHistory(messages []Message, api, endpoint, model string) []Message {
	out := repairToolHistory(stripAuthored(cancelledReplies(messages)))
	for i := range out {
		if !replayMatches(out[i], api, endpoint, model) {
			out[i].Replay = nil
		}
	}
	return wireToolCallIDs(out, api)
}

// cancelledReplyNote marks, in requests only, a reply the user stopped while it
// was streaming. The stored message keeps exactly what the user saw.
const cancelledReplyNote = "\n\n[response interrupted by the user]"

// cancelledReplies renders replies recorded on a user stop. One with no text is
// dropped: its thinking is display-only and an empty assistant turn is invalid
// for several providers. Tool messages use the same stop reason and pass through.
func cancelledReplies(messages []Message) []Message {
	var out []Message
	for i, m := range messages {
		if m.Role != "assistant" || m.StopReason != StopReasonCancelled {
			if out != nil {
				out = append(out, m)
			}
			continue
		}
		if out == nil {
			out = append(make([]Message, 0, len(messages)), messages[:i]...)
		}
		if len(m.ToolCalls) == 0 && strings.TrimSpace(m.TextContent()) == "" {
			continue
		}
		if m.Content != "" {
			m.Content += cancelledReplyNote
		}
		out = append(out, m)
	}
	if out == nil {
		return messages
	}
	return out
}

// responsesMaxCallID is the Responses API limit for function_call.call_id.
const responsesMaxCallID = 64

// wireToolCallIDs rewrites stored tool call IDs that the target protocol rejects.
// Imported pi-ai history stores Responses calls as "call_id|item_id"; the part before
// "|" is the provider-issued call_id. Valid IDs pass through unchanged, so native
// requests stay byte-identical. Gemini is left alone: its encoder looks up thought
// signatures by the stored ID. Request observers see the stored IDs, not these.
func wireToolCallIDs(msgs []Message, api string) []Message {
	if api != APIResponses && api != APIMessages {
		return msgs
	}
	wire := map[string]string{}  // stored ID -> wire ID
	owner := map[string]string{} // wire ID -> stored ID
	mapID := func(id string) string {
		if id == "" {
			return id
		}
		if mapped, ok := wire[id]; ok {
			return mapped
		}
		mapped := id
		if prefix, _, ok := strings.Cut(id, "|"); ok {
			mapped = prefix
		}
		if !validWireToolCallID(mapped, api) {
			mapped = hashedToolCallID(id)
		}
		for attempt := 0; ; attempt++ {
			if prev, taken := owner[mapped]; !taken || prev == id {
				break
			}
			if attempt == 0 {
				mapped = hashedToolCallID(id)
			} else {
				mapped = hashedToolCallID(id + "\x00" + strconv.Itoa(attempt))
			}
		}
		wire[id], owner[mapped] = mapped, id
		return mapped
	}
	for i := range msgs {
		msgs[i].ToolCallID = mapID(msgs[i].ToolCallID)
		if len(msgs[i].ToolCalls) == 0 {
			continue
		}
		calls := make([]ToolCall, len(msgs[i].ToolCalls))
		copy(calls, msgs[i].ToolCalls)
		for j := range calls {
			calls[j].ID = mapID(calls[j].ID)
		}
		msgs[i].ToolCalls = calls
	}
	return msgs
}

func validWireToolCallID(id string, api string) bool {
	if id == "" {
		return false
	}
	if api == APIResponses {
		return len(id) <= responsesMaxCallID
	}
	// Anthropic tool_use.id: ^[a-zA-Z0-9_-]+$
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func hashedToolCallID(id string) string {
	sum := sha256.Sum256([]byte(id))
	return "call_" + hex.EncodeToString(sum[:16])
}
