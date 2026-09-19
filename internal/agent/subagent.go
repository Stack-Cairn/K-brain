package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func subagentPrompt() string {
	wd, _ := os.Getwd()
	return "You are a subagent inside k-brain, a coding agent harness. Complete the task you are given using your tools (bash, read, write, edit), then reply with a concise final report — that report is the only thing the caller sees, so include every finding or result that matters. Do not ask questions; make reasonable assumptions. The caller (or the user) may send you additional guidance mid-task as user messages — fold it into the work.\n\nCurrent working directory: " + wd
}

type SubModel struct {
	Client       ai.Client
	Model        string
	ContextLimit int
	MaxTokens    int
	Effort       string
}

func (a *Agent) newSub(o SubModel) *Agent {

	effort := o.Effort
	if o.Client == nil {
		o = a.TaskDefault
	}
	if o.Client == nil {
		o = SubModel{Client: a.Client, Model: a.Model, ContextLimit: a.ContextLimit}
	}
	if o.MaxTokens == 0 {
		o.MaxTokens = a.MaxTokens
	}

	a.clientMu.Lock()
	o.Client = o.Client.Clone()
	a.clientMu.Unlock()
	sub := New(o.Client, o.Model, o.MaxTokens, subagentPrompt())
	sub.planMode = a.planMode

	if effort != "" {
		sub.Effort = effort
	} else {
		sub.Effort = a.Effort
	}
	sub.ContextLimit = o.ContextLimit
	sub.Tools = tools.All()

	sub.usageSink = a.AddSubUsage
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
			"Launch a subagent to handle a self-contained task with its own fresh context. It has the same tools as you (bash, read, write, edit) and returns only its final report. Use it for context-heavy exploration or work that can be described completely up front. To investigate several things in parallel, emit MULTIPLE subagent calls in one message — they run concurrently and all reports come back together in one turn. background=true is for fire-and-forget tasks you check on later: it runs concurrently while you keep working and the report arrives automatically as a message when it finishes (do NOT poll; subagent_steer can send mid-course corrections). Subagents run on a cheap fast model by default; set model (and optionally effort) only when the task needs a specific or stronger reasoning pass.",
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
			o, err := parent.resolveSub(a.Model, a.Provider)
			if err != nil {

				return "Error: model override: " + err.Error(), nil
			}

			o.Effort = a.Effort

			useWorktree := parent.WorktreeSubagents
			if a.Worktree != nil {
				useWorktree = *a.Worktree
			}
			prompt := a.Prompt

			if a.Background {

				t := parent.RegisterBackground(desc, prompt, o)
				wtPath := ""
				if useWorktree {
					if p, err := provisionSubagentWorktree(ctx, "sub"); err == nil {
						wtPath = p
					}

				}
				parent.LaunchBackground(t, wtPath)
				if wtPath != "" {
					return fmt.Sprintf("Started background subagent %s in worktree %s: %s. Its edits are isolated from your working tree. Do not poll for it.", t.ID, wtPath, desc), nil
				}
				return fmt.Sprintf("Started background subagent %s: %s. Keep working on something else; the report will arrive as a message when it finishes. Do not poll for it.", t.ID, desc), nil
			}
			sub := parent.newSub(o)
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
	return s[:subagentReportCap] + fmt.Sprintf("\n\n... [report truncated — %d bytes total; the investigation covered more than fits here]", len(s))
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
	if t.Status != TaskRunning {
		return fmt.Errorf("subagent %s already %s — steering only reaches a running subagent", id, t.Status)
	}
	if t.sub == nil {
		return fmt.Errorf("subagent %s is not live", id)
	}
	t.sub.Steer(text)
	return nil
}

func (a *Agent) FollowupTask(ctx context.Context, id, text string, ev Events) (string, error) {
	r := a.Tasks()
	t, ok := r.Get(id)
	if !ok {
		return "", fmt.Errorf("unknown subagent %q", id)
	}
	if t.Status == TaskRunning {
		return "", fmt.Errorf("subagent %s is still running — steer it instead", id)
	}
	if t.sub == nil {
		return "", fmt.Errorf("subagent %s is not live (restored from a previous session)", id)
	}
	out, err := t.sub.Turn(ctx, text, ev)

	r.refreshTranscript(id, t.sub)
	return out, err
}
