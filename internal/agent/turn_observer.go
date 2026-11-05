package agent

import (
	"context"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

type userMessageObserverKey struct{}
type userMessageIDKey struct{}
type turnSnapshotKey struct{}

// WithTurnSnapshot attaches request-only text to the user message of the next turn.
// The model sees it after the user's words; history and transcripts keep only what the
// user typed. Desktop slash commands use it to send an expanded prompt template while
// the conversation still shows "/name args".
func WithTurnSnapshot(ctx context.Context, snapshot ai.PromptSnapshot) context.Context {
	return context.WithValue(ctx, turnSnapshotKey{}, snapshot)
}

func withoutTurnSnapshot(ctx context.Context) context.Context {
	if _, ok := turnSnapshot(ctx); !ok {
		return ctx
	}
	return context.WithValue(ctx, turnSnapshotKey{}, ai.PromptSnapshot{})
}

func turnSnapshot(ctx context.Context) (ai.PromptSnapshot, bool) {
	snapshot, ok := ctx.Value(turnSnapshotKey{}).(ai.PromptSnapshot)
	return snapshot, ok && snapshot.Text != ""
}

type memoryOffAuthoredKey struct{}

// WithMemoryOffAuthored marks the turn's user message as written while memory was off,
// for input that waited (a queued message) until after memory was turned back on.
func WithMemoryOffAuthored(ctx context.Context) context.Context {
	return context.WithValue(ctx, memoryOffAuthoredKey{}, true)
}

func memoryOffAuthored(ctx context.Context) bool {
	off, _ := ctx.Value(memoryOffAuthoredKey{}).(bool)
	return off
}

type backgroundNoticeKey struct{}

// WithBackgroundNotice marks the turn's user message as a notice from background
// work (a wait, a process or subagent finishing) rather than something the user
// wrote: it is not authored and carries ai.OriginBackground.
func WithBackgroundNotice(ctx context.Context) context.Context {
	return context.WithValue(ctx, backgroundNoticeKey{}, true)
}

func backgroundNotice(ctx context.Context) bool {
	notice, _ := ctx.Value(backgroundNoticeKey{}).(bool)
	return notice
}

func WithUserMessageID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, userMessageIDKey{}, id)
}

// WithUserMessageObserver observes the durable turn identity before model or tool execution.
func WithUserMessageObserver(ctx context.Context, observe func(ai.Message) error) context.Context {
	return context.WithValue(ctx, userMessageObserverKey{}, observe)
}

func observeUserMessage(ctx context.Context, message ai.Message) error {
	if observe, ok := ctx.Value(userMessageObserverKey{}).(func(ai.Message) error); ok {
		return observe(message)
	}
	return nil
}
