package agent

import (
	"context"

	"github.com/Stack-Cairn/K-brain/internal/hooks"
	"github.com/Stack-Cairn/K-brain/internal/memory"
	"github.com/Stack-Cairn/K-brain/internal/sandbox"
)

func (a *Agent) StartSession(ctx context.Context) error {
	a.startMu.Lock()
	defer a.startMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	id := a.SessionIDValue()
	if a.sessionStarted && a.startedSessionID == id {
		return nil
	}
	if a.sessionStarted && a.startedSessionID != "" {
		// The agent moved to another session: the previous one ends first. A session
		// that started before it had an ID is the same session, now identified.
		_ = a.runHook(ctx, hooks.Event{Name: hooks.SessionEnd, SessionID: a.startedSessionID, Reason: "switch"})
		a.sessionStarted = false
	}
	if err := a.runHook(ctx, hooks.Event{Name: hooks.SessionStart}); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	a.sessionStarted, a.startedSessionID = true, id
	a.RefreshMemory()
	return nil
}

// EndSession runs the SessionEnd hook for a session StartSession started; it does
// nothing otherwise. reason is reported to the hook ("exit", "close", ...). A later
// StartSession starts the session again. Hook failures are returned for logging;
// they cannot keep the session open.
func (a *Agent) EndSession(ctx context.Context, reason string) error {
	a.startMu.Lock()
	defer a.startMu.Unlock()
	if !a.sessionStarted {
		return nil
	}
	a.sessionStarted = false
	return a.runHook(ctx, hooks.Event{Name: hooks.SessionEnd, SessionID: a.startedSessionID, Reason: reason})
}

func (a *Agent) runHook(ctx context.Context, event hooks.Event) error {
	if event.SessionID == "" {
		event.SessionID = a.SessionIDValue()
	}
	event.CWD = a.WorkingDir
	if a.SandboxPolicy != nil && sandbox.FromContext(ctx) == nil {
		ctx = sandbox.WithPolicy(ctx, a.SandboxPolicy)
	}
	if err := a.Hooks.Run(ctx, event); err != nil {
		return err
	}
	if a.PluginHook != nil {
		return a.PluginHook(ctx, event)
	}
	return nil
}

func (a *Agent) RefreshMemory() {
	if a.memoryDisabled || !a.MemoryEnabled() {
		return
	}
	block := memory.PromptBlock(memory.Installation(), memory.Session(a.SessionIDValue()))
	if a.markdownMemoryReadOnly {
		// Installation entries were imported into the runtime store; session
		// notes stay with the conversation that wrote them.
		block = memory.ReadOnlyPromptBlock(memory.Session(a.SessionIDValue()))
	}
	a.msgsMu.Lock()
	defer a.msgsMu.Unlock()
	a.memoryBlock = block
}
