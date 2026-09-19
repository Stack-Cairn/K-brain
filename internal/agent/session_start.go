package agent

import (
	"context"
	"strings"

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
	if err := a.runHook(ctx, hooks.Event{Name: "SessionStart"}); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	a.sessionStarted, a.startedSessionID = true, id
	a.RefreshMemory()
	return nil
}

func (a *Agent) runHook(ctx context.Context, event hooks.Event) error {
	event.SessionID, event.CWD = a.SessionIDValue(), a.WorkingDir
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
	if a.memoryDisabled {
		return
	}
	block := memory.PromptBlock(memory.Installation(), memory.Session(a.SessionIDValue()))
	a.msgsMu.Lock()
	defer a.msgsMu.Unlock()
	if len(a.Messages) == 0 || a.Messages[0].Role != "system" {
		return
	}
	a.Messages[0].Content = strings.TrimSuffix(a.Messages[0].Content, a.memoryBlock) + block
	a.memoryBlock = block
}
