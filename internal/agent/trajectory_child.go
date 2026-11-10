package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// ErrSubagentStopped ends a foreground subagent the user stopped; the parent
// turn keeps running and sees it as a failed tool call.
var ErrSubagentStopped = errors.New("subagent stopped by the user")

// ChildInfo describes a foreground subagent to the request observer.
type ChildInfo struct {
	ID               string
	Description      string
	Prompt           string
	Model            string
	Provider         string
	ParentToolCallID string
	// Progress returns the subagent's transcript and usage so far; it is safe to
	// call while the subagent runs, so observers can save live progress.
	Progress func() ([]ai.Message, ai.Usage)
}

// ChildResult is what a foreground subagent turn ended with.
type ChildResult struct {
	Err     error
	Stopped bool
	Usage   ai.Usage
}

type childRequestObserver interface {
	// ChildStart receives stop, which ends this subagent alone.
	ChildStart(ctx context.Context, info ChildInfo, stop func())
	ChildEnd(ctx context.Context, info ChildInfo, result ChildResult)
	ChildEvents(string) Events
}

func (a *Agent) observedChildTurn(ctx context.Context, description, prompt string) (string, error) {
	id := fmt.Sprintf("foreground-%d", taskIDCounter.Add(1))
	info := ChildInfo{ID: id, Description: description, Prompt: prompt, Model: a.Model, Provider: a.Provider, ParentToolCallID: tools.ToolCallID(ctx),
		Progress: func() ([]ai.Message, ai.Usage) { return a.MessagesSnapshot(), a.TotalUsage() }}
	ctx = withRequestTask(ctx, id)
	parent := ctx
	ctx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	events := Events{}
	observer, _ := ctx.Value(requestObserverKey{}).(childRequestObserver)
	if observer != nil {
		observer.ChildStart(ctx, info, func() { stop(ErrSubagentStopped) })
		events = observer.ChildEvents(id)
	}
	result, err := a.Turn(ctx, prompt, events)
	stopped := parent.Err() == nil && errors.Is(context.Cause(ctx), ErrSubagentStopped)
	if stopped {
		err = ErrSubagentStopped
	}
	if observer != nil {
		observer.ChildEnd(ctx, info, ChildResult{Err: err, Stopped: stopped, Usage: a.TotalUsage()})
	}
	return result, err
}
