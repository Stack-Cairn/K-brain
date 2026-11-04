package agent

import (
	"context"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

// MaxOutputLimitContinuations caps how many times one turn resumes a reply that
// stopped at the output-token limit. After the cap the truncated reply ends the turn.
const MaxOutputLimitContinuations = 2

const (
	outputLimitContinueText = "Your previous response was cut off because it reached the output token limit. " +
		"Continue exactly where it stopped. Do not repeat text you already wrote."
	outputLimitToolCallText = "Your previous response was cut off because it reached the output token limit while you were writing a tool call. " +
		"That tool call was discarded and was NOT executed. Re-issue it now in smaller pieces: " +
		"split large file contents or edits across several smaller tool calls instead of one large call."
)

// WithOutputLimitContinuation enables automatic continuation of replies cut off by
// the output-token limit (see MaxOutputLimitContinuations).
func WithOutputLimitContinuation() Option {
	return func(a *Agent) { a.ContinueOnOutputLimit = true }
}

// continueAfterOutputLimit reports whether the turn should run another round to
// resume msg. A truncated reply never carries tool calls: the stream adapters drop
// the half-written ones, so nothing of that reply is executed.
func (a *Agent) continueAfterOutputLimit(ctx context.Context, msg ai.Message, rounds, done int) bool {
	return a.ContinueOnOutputLimit &&
		msg.StopReason == ai.StopReasonLength &&
		len(msg.ToolCalls) == 0 &&
		done < MaxOutputLimitContinuations &&
		(a.MaxTurns <= 0 || rounds < a.MaxTurns) &&
		ctx.Err() == nil
}

// outputLimitReminder is the user message that asks the model to resume msg.
func outputLimitReminder(msg ai.Message) ai.Message {
	text := outputLimitContinueText
	if ai.TruncatedToolCalls(msg) {
		text = outputLimitToolCallText
	}
	now := time.Now()
	return ai.Message{Role: "user", Content: WrapReminder(text), SentAt: &now}
}
