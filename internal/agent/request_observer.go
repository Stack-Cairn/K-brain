package agent

import (
	"context"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

type RequestObservation struct {
	ToolCallID   string
	TaskID       string
	ParentTaskID string
	Provider     string
	Endpoint     string
	Request      ai.Request
}

type RequestObserver interface {
	Start(context.Context, RequestObservation) (context.Context, error)
	End(context.Context, ai.Message, ai.Usage, error)
	Retry(context.Context, ai.RetryEvent)
}

type requestObserverKey struct{}
type requestTaskKey struct{}
type requestToolKey struct{}
type requestTaskScope struct{ ID, Parent string }

func WithRequestObserver(ctx context.Context, observer RequestObserver) context.Context {
	return context.WithValue(ctx, requestObserverKey{}, observer)
}

func withRequestTask(ctx context.Context, id string) context.Context {
	parent, _ := ctx.Value(requestTaskKey{}).(requestTaskScope)
	return context.WithValue(ctx, requestTaskKey{}, requestTaskScope{ID: id, Parent: parent.ID})
}

func (a *Agent) streamObserved(ctx context.Context, request ai.Request, ev Events) (ai.Message, ai.Usage, error) {
	request.Messages = withPromptContext(request.Messages)
	if a.turnTimeContext {
		request.Messages = withTurnTimes(request.Messages)
	}
	observer, _ := ctx.Value(requestObserverKey{}).(RequestObserver)
	if observer != nil {
		scope, _ := ctx.Value(requestTaskKey{}).(requestTaskScope)
		var err error
		toolCallID, _ := ctx.Value(requestToolKey{}).(string)
		ctx, err = observer.Start(ctx, RequestObservation{ToolCallID: toolCallID, TaskID: scope.ID, ParentTaskID: scope.Parent, Provider: a.Provider, Endpoint: a.Client.Endpoint(), Request: request})
		if err != nil {
			return ai.Message{}, ai.Usage{}, err
		}
		original := ev.OnRetry
		ev.OnRetry = func(event ai.RetryEvent) {
			observer.Retry(ctx, event)
			if original != nil {
				original(event)
			}
		}
	}
	if ev.OnHostedSearch != nil {
		ctx = ai.WithSearchObserver(ctx, ev.OnHostedSearch)
	}
	clearRetry := a.reportRetries(ev)
	defer clearRetry()
	if ev.OnPhase != nil {
		ev.OnPhase("waiting_model")
	}
	var partial partialReply
	message, usage, err := a.Client.Stream(ctx, request, partial.text(ev.OnText), partial.think(ev.OnThink), ev.OnToolCall)
	if observer != nil {
		observer.End(ctx, message, usage, err)
	}
	if err != nil && ctx.Err() != nil {
		// Clients return no message on error; a user stop keeps what was already
		// streamed so the turn loop can record it.
		message = partial.message()
	}
	return message, usage, err
}

// partialReply collects the text and thinking a stream has already delivered.
// Tool calls are left out: a call cut off mid-stream was never complete or run.
type partialReply struct {
	content, reasoning strings.Builder
}

func (p *partialReply) text(next func(string)) func(string) {
	return func(delta string) {
		p.content.WriteString(delta)
		if next != nil {
			next(delta)
		}
	}
}

func (p *partialReply) think(next func(string)) func(string) {
	return func(delta string) {
		p.reasoning.WriteString(delta)
		if next != nil {
			next(delta)
		}
	}
}

// message is the cancelled reply, or a zero Message when nothing was streamed.
func (p *partialReply) message() ai.Message {
	content, reasoning := p.content.String(), p.reasoning.String()
	if strings.TrimSpace(content) == "" {
		content = ""
	}
	if strings.TrimSpace(reasoning) == "" {
		reasoning = ""
	}
	if content == "" && reasoning == "" {
		return ai.Message{}
	}
	return ai.Message{Role: "assistant", Content: content, Reasoning: reasoning, StopReason: ai.StopReasonCancelled}
}

func withPromptContext(messages []ai.Message) []ai.Message {
	out := append([]ai.Message(nil), messages...)
	for i, message := range out {
		if context := promptContextText(message); context != "" {
			out[i].Content = message.Content + "\n\n<kbrain-context>\n" + context + "\n</kbrain-context>"
			out[i].PromptContext = ""
		}
		out[i].PromptSnapshots = nil
	}
	return out
}

// WithTurnTimeContext supplies current context alongside a clock-free system prompt.
func WithTurnTimeContext() Option {
	return func(a *Agent) { a.turnTimeContext = true }
}

// Render persisted timestamps only in requests, without changing displayed history.
// UTC avoids prefix changes when a session reloads in a different local timezone.
func withTurnTimes(messages []ai.Message) []ai.Message {
	out := append([]ai.Message(nil), messages...)
	for i, message := range out {
		if message.Role == "user" && message.Authored && message.SentAt != nil && !message.SentAt.IsZero() {
			out[i].Content = "<kbrain-turn-time>" + message.SentAt.UTC().Format(time.RFC3339) + "</kbrain-turn-time>\n\n" + message.Content
		}
	}
	return out
}
