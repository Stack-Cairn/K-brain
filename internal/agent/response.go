package agent

import "github.com/Stack-Cairn/K-brain/internal/ai"

func (a *Agent) appendResponse(msg ai.Message, usage ai.Usage) {
	msg.Usage = &usage
	msg.Model = a.Model + " @ " + a.Provider
	a.msgsMu.Lock()
	a.Messages = append(a.Messages, msg)
	a.msgsMu.Unlock()
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
