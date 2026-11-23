package agent

import (
	"context"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func (a *Agent) appendResponse(msg ai.Message, usage ai.Usage) {
	if msg.ID == "" {
		msg.ID = newMessageID()
	}
	if msg.SentAt == nil {
		now := time.Now()
		msg.SentAt = &now
	}
	msg.Usage = &usage
	msg.Model = a.Model + " @ " + a.Provider
	a.msgsMu.Lock()
	a.Messages = append(a.Messages, msg)
	a.msgsMu.Unlock()
}

// appendInterruptedReply records the part of a reply that streamed before the
// user stopped the turn or the stream failed, so history keeps what the user
// already saw. A failed reply keeps the error text in Error. It adds nothing
// when nothing was streamed.
func (a *Agent) appendInterruptedReply(ctx context.Context, msg ai.Message, usage ai.Usage) {
	if msg.Role != "assistant" {
		return
	}
	switch {
	case msg.StopReason == ai.StopReasonCancelled && ctx.Err() != nil:
	case msg.StopReason == ai.StopReasonError && msg.Error != "" && ctx.Err() == nil:
	default:
		return
	}
	a.appendResponse(msg, usage)
}

func (a *Agent) LastStopReason() ai.StopReason {
	a.msgsMu.Lock()
	defer a.msgsMu.Unlock()
	for i := len(a.Messages) - 1; i >= 0; i-- {
		if a.Messages[i].Role == "assistant" {
			return a.Messages[i].StopReason
		}
	}
	return ""
}
