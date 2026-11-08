package agent

import (
	"slices"

	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// Foreground tracks this agent's foreground shell commands that the user can
// move to the background while they run.
func (a *Agent) Foreground() *tools.Foreground { return a.fg }

// SetManagedProcesses makes m take over the commands the user moves to the
// background, instead of a manager the agent creates and owns. The caller
// owns m: it delivers its exit notices and closes it.
func (a *Agent) SetManagedProcesses(m *tools.ManagedProcessManager) {
	a.procMu.Lock()
	defer a.procMu.Unlock()
	if a.ownsProcs && a.procs != nil {
		a.procs.CloseAll()
	}
	a.procs, a.ownsProcs = m, false
}

// managedProcesses returns the manager that takes over a command the user
// moves to the background. An agent without one creates its own, which
// reports exits through Notify and adds a ManagedProcess tool to the catalog
// so the model can read and stop what it took over.
func (a *Agent) managedProcesses() *tools.ManagedProcessManager {
	a.procMu.Lock()
	if a.procs == nil {
		a.procs, a.ownsProcs = tools.NewManagedProcessManager(), true
		a.procs.SetExitNotifier(a.Notify)
	}
	m, owns := a.procs, a.ownsProcs
	a.procMu.Unlock()
	if owns {
		a.toolsMu.Lock()
		if a.processTool == nil {
			tool := tools.BackgroundProcessTool(m)
			tool.NoInherit = true
			a.processTool = &tool
		}
		a.toolsMu.Unlock()
	}
	return m
}

// CloseManagedProcesses stops the commands the user moved to the background,
// when the agent owns the manager that took them over, and forgets them: the
// next command moved to the background starts a fresh manager.
func (a *Agent) CloseManagedProcesses() {
	a.procMu.Lock()
	m, owns := a.procs, a.ownsProcs
	if owns {
		a.procs, a.ownsProcs = nil, false
	}
	a.procMu.Unlock()
	if !owns || m == nil {
		return
	}
	a.toolsMu.Lock()
	a.processTool = nil
	a.toolsMu.Unlock()
	m.CloseAll()
}

// withProcessToolLocked adds the ManagedProcess tool for backgrounded commands
// once there is one, unless the catalog already has such a tool. The caller
// holds toolsMu.
func (a *Agent) withProcessToolLocked(ts []tools.Tool) []tools.Tool {
	if a.processTool == nil || slices.ContainsFunc(ts, func(t tools.Tool) bool { return t.Def.Function.Name == a.processTool.Def.Function.Name }) {
		return ts
	}
	return append(ts, *a.processTool)
}
