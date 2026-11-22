package agent

import (
	"context"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

// WithSystemPromptResolver refreshes factory instructions before each turn.
func WithSystemPromptResolver(resolve func() (string, error)) Option {
	return func(a *Agent) { a.systemPromptResolver = resolve }
}

// SetPromptAddendum sets a function whose text is appended to the resolved
// system prompt before each turn (plugin prompts, client-specific hints). It
// applies only to agents built with WithSystemPromptResolver, which rebuild
// the system prompt every turn; nil removes it.
func (a *Agent) SetPromptAddendum(addendum func() string) {
	a.msgsMu.Lock()
	a.promptAddendum = addendum
	a.msgsMu.Unlock()
}

type promptAdditionKey struct{}

// WithPromptAddition appends text to the system prompt of turns run with ctx
// (for agents with a system prompt resolver). It lets one run add guidance
// that other runs of the same agent do not see.
func WithPromptAddition(ctx context.Context, text string) context.Context {
	if text == "" {
		return ctx
	}
	previous, _ := ctx.Value(promptAdditionKey{}).(string)
	return context.WithValue(ctx, promptAdditionKey{}, previous+text)
}

func (a *Agent) refreshSystemPrompt(ctx context.Context) error {
	if a.systemPromptResolver == nil {
		return nil
	}
	prompt, err := a.systemPromptResolver()
	if err != nil {
		return err
	}
	a.msgsMu.Lock()
	addendum := a.promptAddendum
	a.msgsMu.Unlock()
	if addendum != nil {
		prompt += addendum()
	}
	if addition, _ := ctx.Value(promptAdditionKey{}).(string); addition != "" {
		prompt += addition
	}
	a.msgsMu.Lock()
	defer a.msgsMu.Unlock()
	if len(a.Messages) > 0 && a.Messages[0].Role == "system" {
		a.Messages[0].Content = prompt
	} else {
		a.Messages = append([]ai.Message{{Role: "system", Content: prompt}}, a.Messages...)
	}
	return nil
}
