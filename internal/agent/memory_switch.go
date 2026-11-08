package agent

import (
	"slices"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

const (
	// memoryOffNotice is sent once per session while memory is off: it retracts memory
	// earlier turns carried and tells the model not to keep memory any other way.
	memoryOffNotice = "\n\n<memory>Memory is turned off by the user. Earlier saved-memory and memory index snapshots no longer apply; do not rely on them. Do not save memories in any way, including writing memory.md or other files. When asked to remember something, tell the user memory is turned off.</memory>"
	// memoryOnNotice lifts the off instruction when memory is turned back on.
	memoryOnNotice = "\n\n<memory>Memory is turned on again; the earlier instruction not to save memory no longer applies.</memory>"
	// memorySwitchSource carries the on-again instruction apart from the memory blocks.
	memorySwitchSource = "memory-switch"
	// memoryOffSource marks, with an empty snapshot, every user message created while
	// memory is off. It is persisted with the message so the extractor never sees those
	// turns, also after re-enabling or reloading the session.
	memoryOffSource = "memory-off"
)

// WithMemoryEnabled sets the memory master switch at construction.
func WithMemoryEnabled(enabled bool) Option {
	return func(a *Agent) { a.SetMemoryEnabled(enabled) }
}

// SetMemoryEnabled toggles memory injection, hidden extraction and memory tools from the
// next turn on. Stored memories are untouched.
func (a *Agent) SetMemoryEnabled(enabled bool) {
	a.memoryOff.Store(!enabled)
	if !enabled {
		a.msgsMu.Lock()
		a.memoryBlock, a.runtimeMemoryBlock = "", ""
		a.msgsMu.Unlock()
	}
}

func (a *Agent) MemoryEnabled() bool { return !a.memoryOff.Load() }

// KnownTools are the tools a run may name in its options, including memory tools the
// switch currently hides, so a run's validation does not depend on the switch.
func (a *Agent) KnownTools() []tools.Tool {
	a.toolsMu.Lock()
	defer a.toolsMu.Unlock()
	return a.modeTools(append(append(a.ownToolsLocked(), a.mcpTools...), a.pluginTools...))
}

func (a *Agent) withoutMemoryTools(all []tools.Tool) []tools.Tool {
	if a.MemoryEnabled() {
		return all
	}
	return slices.DeleteFunc(all, func(tool tools.Tool) bool { return tool.Memory })
}

// MemoryOffSnapshot marks a user message created while memory is off. Every path that
// adds a user message to History while off must attach it.
func MemoryOffSnapshot() ai.PromptSnapshot { return ai.PromptSnapshot{Source: memoryOffSource} }

func (a *Agent) memoryOffMarks() []ai.PromptSnapshot {
	if a.MemoryEnabled() {
		return nil
	}
	return []ai.PromptSnapshot{MemoryOffSnapshot()}
}

func isMemoryOffMessage(m ai.Message) bool {
	return m.Role == "user" && slices.ContainsFunc(m.PromptSnapshots, func(s ai.PromptSnapshot) bool {
		return s.Source == memoryOffSource
	})
}

// markResumedMessage writes this turn's memory snapshots into a resumed History message
// the switch has touched (memory is off now, or the message was written while off).
// contextSnapshots already diffed against History including this message, so computed
// holds only changes: a retraction, fresh memory, or nothing. The off marker is added
// once and never removed. The on-again instruction is written back whenever it was
// computed. The persisted message, not the turn's local copy, is what later turns and
// the extractor read.
func markResumedMessage(resumed *ai.Message, computed []ai.PromptSnapshot, enabled bool) {
	off := isMemoryOffMessage(*resumed)
	touched := !enabled || off
	snapshots := slices.Clone(resumed.PromptSnapshots)
	for _, s := range computed {
		switch s.Source {
		case memorySwitchSource:
			// The on-again instruction follows the switch whatever the message saw.
			snapshots = append(snapshots, s)
		case savedMemorySource, runtimeMemorySource:
			if touched {
				snapshots = append(snapshots, s)
			}
		case memoryOffSource:
			if !off {
				snapshots = append(snapshots, s)
			}
		}
	}
	resumed.PromptSnapshots = snapshots
}

// withoutMemoryOffTurns drops user messages created while memory was off from the
// history handed to the extractor.
func withoutMemoryOffTurns(history []ai.Message) []ai.Message {
	return slices.DeleteFunc(history, isMemoryOffMessage)
}
