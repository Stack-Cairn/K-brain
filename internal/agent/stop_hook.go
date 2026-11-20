package agent

import (
	"context"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/hooks"
)

// MaxStopHookBlocks caps how many times Stop hooks may keep one turn going.
// The Stop hooks still run when the cap is reached, but their block is ignored.
const MaxStopHookBlocks = 5

const promptHookSource = "user-prompt-hook"

// stopGate runs the Stop hooks for a turn that is about to complete. It
// reports the reason and true when a hook blocked the stop and the turn should
// run another round; blocks counts the blocks so far. ran is set once the Stop
// hooks have run for a stop that stands, so they do not run again on exit.
func (a *Agent) stopGate(ctx context.Context, blocks *int, ran *bool) (string, bool) {
	if a.Hooks == nil && a.PluginHook == nil {
		return "", false
	}
	outcome, err := a.runHookOutcome(ctx, hooks.Event{Name: hooks.Stop, StopHookActive: *blocks > 0})
	if err != nil || !outcome.Block || *blocks >= MaxStopHookBlocks || ctx.Err() != nil {
		// A failing Stop hook never keeps the turn going. A block past the cap
		// still has the plugin hook observe the final stop.
		if err == nil && outcome.Block && a.PluginHook != nil {
			_ = a.PluginHook(ctx, a.stopEvent(*blocks > 0))
		}
		*ran = true
		return "", false
	}
	*blocks++
	return outcome.Reason, true
}

func (a *Agent) stopEvent(active bool) hooks.Event {
	return hooks.Event{Name: hooks.Stop, SessionID: a.SessionIDValue(), CWD: a.WorkingDir, StopHookActive: active}
}

func stopBlockText(reason string) string {
	if reason == "" {
		reason = "no reason given"
	}
	return "A Stop hook blocked ending the turn. Address the following before finishing:\n" + reason
}

// promptHookSnapshot carries UserPromptSubmit additionalContext as request-only
// context on the turn's user message.
func promptHookSnapshot(text string) ai.PromptSnapshot {
	if text == "" {
		return ai.PromptSnapshot{}
	}
	return ai.PromptSnapshot{Source: promptHookSource, Text: "\n\n<hook-context>\n" + text + "\n</hook-context>"}
}
