package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/hooks"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func (a *Agent) trackTool(name string, delta int64) {
	if name == "subagent" || name == "subagent_followup" {
		a.subagentInflight.Add(delta)
	} else {
		a.otherInflight.Add(delta)
	}
}

func (a *Agent) WaitingOnSubagents() bool {
	return a.TurnRunning() && a.subagentInflight.Load() > 0 && a.otherInflight.Load() == 0
}

func (a *Agent) runTools(ctx context.Context, calls []ai.ToolCall, ev Events) []tools.Result {
	results := make([]tools.Result, len(calls))
	type outcome struct {
		i    int
		out  tools.Result
		ms   int64
		code int
	}
	outCh := make(chan outcome, len(calls))

	refused := a.markDoomLoops(calls)

	var wg sync.WaitGroup
	for i, tc := range calls {
		wg.Add(1)
		go func(i int, tc ai.ToolCall) {
			defer wg.Done()
			name, args := tc.Function.Name, tc.Function.Arguments
			if ev.OnLifecycle != nil {
				ev.OnLifecycle("tool_execution_start")
				defer ev.OnLifecycle("tool_execution_end")
			}
			if a.Hooks != nil || a.PluginHook != nil {
				if err := a.runHook(ctx, hooks.Event{Name: hooks.PreToolUse, ToolID: tc.ID, ToolName: name, ToolArgs: args}); err != nil {
					out := "Error: hook PreToolUse denied tool call: " + err.Error()
					if ev.OnToolEnd != nil {
						ev.OnToolEnd(tc.ID, name, out)
					}
					outCh <- outcome{i, tools.Result{Text: out, Failed: true, Cancelled: ctx.Err() != nil}, 0, 1}
					return
				}
			}

			if refused[i] {

				out := doomLoopRefusal(name)
				if ev.OnToolStart != nil {
					ev.OnToolStart(tc.ID, name, args)
				}
				if ev.OnToolEnd != nil {
					ev.OnToolEnd(tc.ID, name, out)
				}
				outCh <- outcome{i, tools.Result{Text: out, Failed: true, Cancelled: ctx.Err() != nil}, 0, 1}
				return
			}

			var release func()
			var lockErr error
			if path, ok := toolMutationPath(name, args); ok {
				release, lockErr = a.files.acquirePath(ctx, tools.ResolvePath(ctx, path))
			} else if name == "bash" {
				release, lockErr = a.files.acquireGlobal(ctx)
			}
			if lockErr != nil {
				out := "Error: " + lockErr.Error()
				if ev.OnToolEnd != nil {
					ev.OnToolEnd(tc.ID, name, out)
				}
				outCh <- outcome{i, tools.Result{Text: out, Failed: true, Cancelled: ctx.Err() != nil}, 0, 1}
				return
			}
			if release != nil {
				defer release()
			}

			if ev.OnToolStart != nil {
				ev.OnToolStart(tc.ID, name, args)
			}
			a.trackTool(name, 1)
			defer a.trackTool(name, -1)
			start := time.Now()
			callCtx := tools.WithToolCallID(ctx, tc.ID)
			if ev.OnToolOutput != nil && streamsToolOutput(name) {
				callCtx = tools.WithOnUpdate(callCtx, func(soFar string) {
					ev.OnToolOutput(tc.ID, soFar)
				})
			}
			callCtx = context.WithValue(callCtx, requestToolKey{}, tc.ID)
			var mutated mutationSet
			callCtx = tools.WithFileMutationRecorder(callCtx, mutated.record)
			result := tools.ExecuteResult(callCtx, a.AllTools(), name, json.RawMessage(args), a.Vision)
			out := result.Text
			if a.Hooks != nil || a.PluginHook != nil {
				if err := a.runHook(ctx, hooks.Event{Name: hooks.PostToolUse, ToolID: tc.ID, ToolName: name, ToolArgs: args, ToolResult: out}); err != nil {
					out += "\nHook PostToolUse failed: " + err.Error()
				}
				// PostToolUseFailure adds to PostToolUse for a tool that returned an
				// error; an interrupted call is not a tool failure.
				if result.Failed && !result.Cancelled {
					if err := a.runHook(ctx, hooks.Event{Name: hooks.PostToolUseFailure, ToolID: tc.ID, ToolName: name, ToolArgs: args, ToolResult: result.Text}); err != nil {
						out += "\nHook PostToolUseFailure failed: " + err.Error()
					}
				}
			}
			if len(a.Reminders) > 0 && !result.Cancelled {
				result.Text = out
				out = appendReminders(callCtx, a.Reminders, out, ToolOutcome{ID: tc.ID, Name: name, Args: args, Result: result, MutatedPaths: mutated.snapshot()})
			}
			ms := time.Since(start).Milliseconds()
			if ev.OnToolEnd != nil {
				ev.OnToolEnd(tc.ID, name, out)
			}
			result.Text = out
			outCh <- outcome{i, result, ms, toolExitCode(out)}
		}(i, tc)
	}

	go func() {
		wg.Wait()
		close(outCh)
	}()
	for oc := range outCh {
		results[oc.i] = oc.out
		if ev.OnToolResult != nil {
			ev.OnToolResult(calls[oc.i].ID, calls[oc.i].Function.Name, oc.out)
		}
		calls[oc.i].DurationMs = oc.ms
		if ms := oc.out.DurationMs(); ms > 0 {
			calls[oc.i].DurationMs = ms
		}
		calls[oc.i].ExitCode = oc.code
		if status := oc.out.ExitCode; status != nil {
			code := *status
			calls[oc.i].ExitCode, calls[oc.i].ExitStatus = code, &code
		}
		calls[oc.i].TimedOut = oc.out.TimedOut
		calls[oc.i].QuestionAnswers = oc.out.QuestionAnswers
	}
	return results
}

const doomLoopMaxRun = 3

var doomLoopExempt = map[string]bool{"wait": true}

func (a *Agent) markDoomLoops(calls []ai.ToolCall) []bool {
	refused := make([]bool, len(calls))
	a.loopMu.Lock()
	defer a.loopMu.Unlock()
	for i, tc := range calls {
		key := tc.Function.Name + "\x00" + tc.Function.Arguments
		if key == a.lastCallKey {
			a.lastCallRun++
		} else {
			a.lastCallKey = key
			a.lastCallRun = 1
		}
		if a.lastCallRun >= doomLoopMaxRun && !doomLoopExempt[tc.Function.Name] {
			refused[i] = true
		}
	}
	return refused
}

func doomLoopRefusal(name string) string {
	return fmt.Sprintf("Error: refused to run %s — this exact call (same arguments) has already run %d times in a row with no other tool call in between. Repeating it will not produce new information. Change the approach: adjust the command/arguments, do the work instead of polling for it, or ask the user for guidance.",
		name, doomLoopMaxRun)
}

func toolExitCode(out string) int {
	if strings.HasPrefix(out, "error") || strings.HasPrefix(out, "Error") {
		return 1
	}
	return 0
}

// streamsToolOutput 列出运行中会推送输出快照的工具：CLI 的 bash 与桌面端
// LiveAgent 目录里的 Bash（两者都走 bashrun，快照有上限且按间隔节流）。
func streamsToolOutput(name string) bool {
	return name == "bash" || name == "Bash"
}
