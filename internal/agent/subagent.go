package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/sandbox"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func subagentPrompt(wd string) string {
	if wd == "" {
		wd, _ = os.Getwd()
	}
	return "You are a subagent inside k-brain, a coding agent harness. Complete the task you are given using the tools available to you, then reply with a concise final report — that report is the only thing the caller sees, so include every finding or result that matters. Do not ask questions; make reasonable assumptions. The caller (or the user) may send you additional guidance mid-task as user messages — fold it into the work.\n\nCurrent working directory: " + wd
}

type SubModel struct {
	Client       ai.Client
	Model        string
	Provider     string
	ContextLimit int
	MaxTokens    int
	Effort       string
	Vision       bool
}

func (a *Agent) setWorktree(path string) {
	if path == "" {
		return
	}
	a.WorkingDir = path
	a.SandboxPolicy = a.SandboxPolicy.ForRoot(path)
	a.msgsMu.Lock()
	defer a.msgsMu.Unlock()
	if len(a.Messages) > 0 && a.Messages[0].Role == "system" {
		const marker = "\n\nCurrent working directory: "
		content := a.Messages[0].Content
		if i := strings.Index(content, marker); i >= 0 {
			a.Messages[0].Content = content[:i] + marker + path
		}
	}
}

func (a *Agent) newSub(o SubModel) *Agent {
	a.clientMu.Lock()
	effort := o.Effort
	if o.Client == nil {
		o = a.TaskDefault
	}
	if o.Client == nil {
		o = SubModel{Client: a.Client, Model: a.Model, Provider: a.Provider, ContextLimit: a.ContextLimit, Vision: a.Vision}
	}
	if o.MaxTokens == 0 {
		o.MaxTokens = a.MaxTokens
	}
	if effort == "" {
		effort = o.Effort
	}
	if o.ContextLimit == 0 {
		o.ContextLimit = a.ContextLimit
	}

	o.Client = o.Client.Clone()
	a.clientMu.Unlock()
	sub := New(o.Client, o.Model, o.MaxTokens, subagentPrompt(a.WorkingDir), WithExperimental(a.experimental))
	sub.Provider = o.Provider
	sub.Vision = o.Vision
	sub.planMode = a.planMode
	if a.files != nil {
		sub.files = a.files
	}
	sub.WorkingDir = a.WorkingDir
	if sub.WorkingDir == "" {
		sub.WorkingDir, _ = os.Getwd()
	}
	sub.ResolveModel = a.ResolveModel
	sub.TaskDefault = a.TaskDefault
	sub.WorktreeSubagents = a.WorktreeSubagents
	sub.BrowserDisabled = a.BrowserDisabled
	sub.ComputerDisabled = a.ComputerDisabled
	sub.SandboxPolicy = a.SandboxPolicy
	sub.Hooks = a.Hooks
	sub.PluginHook = a.PluginHook
	sub.memoryDisabled = true
	sub.SetSessionID(a.SessionIDValue())
	sub.sessionStarted, sub.startedSessionID = true, sub.SessionIDValue()
	sub.experimental = append([]string(nil), a.experimental...)
	a.toolsMu.Lock()
	sub.Tools = sub.inheritedTools(a.Tools)
	sub.mcpTools = sub.inheritedTools(a.mcpTools)
	sub.pluginTools = sub.inheritedTools(a.pluginTools)
	a.toolsMu.Unlock()

	if effort != "" {
		sub.Effort = effort
	} else {
		sub.Effort = a.Effort
	}
	sub.ContextLimit = o.ContextLimit

	sub.usageSink = a.AddSubUsage
	return sub
}

func (a *Agent) inheritedTools(source []tools.Tool) []tools.Tool {
	out := make([]tools.Tool, 0, len(source))
	for _, tool := range source {
		if tool.NoInherit ||
			(a.BrowserDisabled && tool.Def.Function.Name == "browser_exec") ||
			(a.ComputerDisabled && tool.Def.Function.Name == "computer_exec") {
			continue
		}
		out = append(out, tool)
	}
	return out
}

func (a *Agent) newSubContext(ctx context.Context, o SubModel) *Agent {
	sub := a.newSub(o)
	if policy := sandbox.FromContext(ctx); policy != nil {
		sub.SandboxPolicy = policy
	}
	return sub
}

func (a *Agent) resolveSub(model, provider string) (SubModel, error) {
	if model == "" {
		return SubModel{}, nil
	}
	if a.ResolveModel == nil {
		return SubModel{}, errors.New("per-task model overrides are not available in this session")
	}
	return a.ResolveModel(model, provider)
}

func taskTool(parent *Agent) tools.Tool {
	return tools.Tool{
		Def: ai.NewTool("subagent",
			"Launch a subagent to handle a self-contained task with its own fresh context. It inherits your available execution tools, including custom, MCP, and plugin tools, but not parent-session tools for questions, task orchestration, todos, or memory. It returns only its final report. Use it for context-heavy exploration or work that can be described completely up front. To investigate several things in parallel, emit MULTIPLE subagent calls in one message — they run concurrently and all reports come back together in one turn. background=true runs concurrently while you keep working and the report arrives automatically as a message when it finishes (do NOT poll; subagent_steer can send mid-course corrections). Subagents use the configured task model, or inherit your model when no task model is configured; set model (and optionally effort) only when the task needs a specific reasoning pass.",
			`{"type":"object","properties":{"description":{"type":"string","description":"Short 3-8 word summary of the task"},"prompt":{"type":"string","description":"Complete instructions for the subagent; it cannot ask follow-up questions"},"background":{"type":"boolean","description":"Run concurrently and get notified on completion (default false = block until done)"},"model":{"type":"string","description":"Optional model to run the subagent on (a configured model name or catalog id); omit for the default"},"provider":{"type":"string","description":"Optional provider for the model override; omit for its default routing"},"effort":{"type":"string","description":"Optional reasoning effort for THIS subagent (e.g. \"low\", \"medium\", \"high\", \"xhigh\"); omit to inherit yours. Raise it for a deep-reasoning verification pass on a stronger model."},"worktree":{"type":"boolean","description":"Run the subagent in its own git worktree so its file edits stay isolated from yours and from other subagents (default: the session's worktreeSubagents setting). Use true for parallel EDITING subagents; leave false for read-only/exploration tasks (worktree is wasted) or when the subagent needs the parent's uncommitted changes."}},"required":["prompt"]}`),
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var a struct {
				Description string `json:"description"`
				Prompt      string `json:"prompt"`
				Background  bool   `json:"background"`
				Model       string `json:"model"`
				Provider    string `json:"provider"`
				Effort      string `json:"effort"`
				Worktree    *bool  `json:"worktree"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
			desc := a.Description
			if desc == "" {
				desc = "subagent task"
			}
			if strings.TrimSpace(a.Prompt) == "" {
				return "", errors.New("subagent prompt is required")
			}
			o, err := parent.resolveSub(a.Model, a.Provider)
			if err != nil {

				return "Error: model override: " + err.Error(), nil
			}

			if a.Effort != "" {
				o.Effort = a.Effort
			}

			useWorktree := parent.WorktreeSubagents
			if a.Worktree != nil {
				useWorktree = *a.Worktree
			}
			prompt := a.Prompt

			if a.Background {

				t := parent.RegisterBackgroundContext(ctx, desc, prompt, o)
				wtPath, err := parent.prepareBackground(t, useWorktree)
				if err != nil {
					return "", err
				}
				if wtPath != "" {
					return fmt.Sprintf("Started background subagent %s in worktree %s: %s. Its edits are isolated from your working tree. Do not poll for it.", t.ID, wtPath, desc), nil
				}
				return fmt.Sprintf("Started background subagent %s: %s. Keep working on something else; the report will arrive as a message when it finishes. Do not poll for it.", t.ID, desc), nil
			}
			if useWorktree {
				wtID := fmt.Sprintf("foreground-%d", taskIDCounter.Add(1))
				wtPath, werr := provisionSubagentWorktreeAt(ctx, parent.WorkingDir, wtID)
				if werr != nil {
					return "", fmt.Errorf("create subagent worktree: %w", werr)
				}
				sub := parent.newSubContext(ctx, o)
				sub.setWorktree(wtPath)
				ctx = sandbox.WithPolicy(ctx, sub.SandboxPolicy)
				report, runErr := sub.Turn(ctx, prompt, Events{})
				if runErr != nil {
					return report, fmt.Errorf("subagent in worktree %s: %w", wtPath, runErr)
				}
				return capReport(report) + "\n\nWorktree: " + wtPath, nil
			}
			sub := parent.newSubContext(ctx, o)
			report, err := sub.Turn(ctx, prompt, Events{})
			if err != nil {
				return report, err
			}
			return capReport(report), nil
		},
	}
}

const subagentReportCap = 50_000

func capReport(s string) string {
	if len(s) <= subagentReportCap {
		return s
	}
	end := subagentReportCap
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end] + fmt.Sprintf("\n\n... [report truncated — %d bytes total; the investigation covered more than fits here]", len(s))
}

func taskSteerTool(parent *Agent) tools.Tool {
	return tools.Tool{
		Def: ai.NewTool("subagent_steer",
			"Send additional guidance to a running background subagent (started with the subagent tool). The message is injected into the subagent's conversation at its next loop boundary. Use it to correct course or add information; it cannot make a finished subagent resume.",
			`{"type":"object","properties":{"id":{"type":"string","description":"The subagent id, e.g. sub-3"},"message":{"type":"string","description":"The guidance to inject"}},"required":["id","message"]}`),
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var a struct {
				ID      string `json:"id"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
			if err := parent.SteerTask(a.ID, a.Message); err != nil {

				return "Error: " + err.Error(), nil
			}
			return fmt.Sprintf("Steered %s; the guidance lands at the subagent's next loop boundary.", a.ID), nil
		},
	}
}

func (a *Agent) SteerTask(id, text string) error {
	r := a.Tasks()
	t, ok := r.Get(id)
	if !ok {
		return fmt.Errorf("unknown subagent %q", id)
	}
	if t.Status != TaskRunning && !t.FollowingUp {
		return fmt.Errorf("subagent %s already %s — steering only reaches a running subagent", id, t.Status)
	}
	if t.sub == nil {
		return fmt.Errorf("subagent %s is not live", id)
	}
	t.sub.Steer(text)
	return nil
}

func (a *Agent) FollowupTask(ctx context.Context, id, text string, ev Events) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if strings.TrimSpace(text) == "" {
		return "", errors.New("subagent follow-up message is required")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	r := a.Tasks()
	r.mu.Lock()
	t, ok := r.tasks[id]
	if !ok {
		r.mu.Unlock()
		return "", fmt.Errorf("unknown subagent %q", id)
	}
	if t.Status == TaskRunning {
		r.mu.Unlock()
		return "", fmt.Errorf("subagent %s is still running — steer it instead", id)
	}
	if t.FollowingUp {
		r.mu.Unlock()
		return "", fmt.Errorf("subagent %s already has a follow-up running", id)
	}
	if t.sub == nil {
		r.mu.Unlock()
		return "", fmt.Errorf("subagent %s is not live (restored from a previous session)", id)
	}
	t.FollowingUp = true
	t.followCancel = cancel
	started := *t
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		t.FollowingUp = false
		t.followCancel = nil
		delete(r.subs, id)
		finished := *t
		r.mu.Unlock()
		if r.OnChange != nil {
			r.OnChange(&finished)
		}
	}()
	stream := r.emitter(id)
	stream.OnSteer(text)
	if r.OnChange != nil {
		r.OnChange(&started)
	}
	out, err := t.sub.Turn(ctx, text, FanIn(stream, ev))
	if err != nil {
		r.emitLocked(id, 4, err.Error(), "", true)
	}

	r.refreshTranscript(id, t.sub)
	return out, err
}
