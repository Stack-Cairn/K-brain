package backend

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
)

// errBackendShutdown is the cancellation cause of a run that the backend's own
// shutdown interrupted. It wraps context.Canceled, so the run ends exactly as
// a stopped one does.
var errBackendShutdown = fmt.Errorf("backend shut down: %w", context.Canceled)

const stoppedMarkerPrefix = "assistant-stopped-"

// withoutStoppedMarker drops a trailing stop marker, so a prompt stopped
// before any reply can still be resumed as the tail user message.
func withoutStoppedMarker(messages []ai.Message) []ai.Message {
	if n := len(messages); n > 0 {
		last := messages[n-1]
		if last.Role == protocol.RoleAssistant && last.StopReason == ai.StopReasonCancelled && strings.HasPrefix(last.ID, stoppedMarkerPrefix) && last.TextContent() == "" && len(last.ToolCalls) == 0 && len(last.Parts) == 0 {
			return messages[:n-1]
		}
	}
	return messages
}

// markStoppedRun records a cancelled run in its history before it is saved.
// A run stopped before any assistant reply gets an empty "assistant-stopped-
// <run>" reply with stop_reason "cancelled", so the stop stays visible after
// reopening the conversation; requests leave it out. When reason is set (the
// backend shut down), it is written on that marker and on the run's cancelled
// reply and tool results.
func markStoppedRun(ag *agent.Agent, runID string, assistantsBefore int, reason string) {
	messages := ag.MessagesSnapshot()
	assistants, first := 0, -1
	for i, message := range messages {
		if message.Role != protocol.RoleAssistant {
			continue
		}
		assistants++
		if assistants == assistantsBefore+1 {
			first = i
		}
	}
	if first < 0 {
		// Only a run whose prompt is the tail gets a marker; a run cancelled
		// before it added its prompt leaves history as it was.
		if len(messages) == 0 || messages[len(messages)-1].Role != protocol.RoleUser {
			return
		}
		now := time.Now().UTC()
		ag.RestoreMessages(append(messages, ai.Message{
			ID:           stoppedMarkerPrefix + runID,
			Role:         protocol.RoleAssistant,
			Content:      "",
			Model:        ag.Model + " @ " + ag.Provider,
			StopReason:   ai.StopReasonCancelled,
			CancelReason: reason,
			SentAt:       &now,
		}))
		return
	}
	if reason == "" {
		return
	}
	changed := false
	for i := first; i < len(messages); i++ {
		if messages[i].StopReason == ai.StopReasonCancelled && (messages[i].Role == protocol.RoleAssistant || messages[i].Role == protocol.RoleTool) {
			messages[i].CancelReason = reason
			changed = true
		}
	}
	if changed {
		ag.RestoreMessages(messages)
	}
}
