package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/tools/bashrun"
)

// Event names. config.HookEvents lists the same set for validation.
const (
	SessionStart       = "SessionStart"
	UserPromptSubmit   = "UserPromptSubmit"
	PreToolUse         = "PreToolUse"
	PostToolUse        = "PostToolUse"
	PostToolUseFailure = "PostToolUseFailure"
	PreCompact         = "PreCompact"
	PostCompact        = "PostCompact"
	Stop               = "Stop"
	SessionEnd         = "SessionEnd"
)

type Event struct {
	Name       string `json:"event"`
	SessionID  string `json:"sessionId,omitempty"`
	CWD        string `json:"cwd,omitempty"`
	Prompt     string `json:"prompt,omitempty"`
	ToolID     string `json:"toolId,omitempty"`
	ToolName   string `json:"toolName,omitempty"`
	ToolArgs   string `json:"toolArgs,omitempty"`
	ToolResult string `json:"toolResult,omitempty"`

	// Trigger is "auto" or "manual" for PreCompact and PostCompact.
	Trigger string `json:"trigger,omitempty"`
	// MessagesBefore and TokensBefore describe the context PreCompact and
	// PostCompact compact; MessagesAfter and TokensAfter the result (PostCompact).
	// Tokens are the agent's estimate.
	MessagesBefore int `json:"messagesBefore,omitempty"`
	TokensBefore   int `json:"tokensBefore,omitempty"`
	MessagesAfter  int `json:"messagesAfter,omitempty"`
	TokensAfter    int `json:"tokensAfter,omitempty"`
	// Summary is the new compaction summary (PostCompact).
	Summary string `json:"summary,omitempty"`
	// Reason says why a session ended (SessionEnd): "exit", "close" or "switch".
	Reason string `json:"reason,omitempty"`
}

// DeniedError is a PreToolUse hook's structured refusal: it exited 0 and printed
// {"decision":"deny","reason":"..."} as its output or as the last output line.
type DeniedError struct {
	Reason string
}

func (e *DeniedError) Error() string {
	if e.Reason == "" {
		return "denied by hook"
	}
	return e.Reason
}

type Runner struct {
	items map[string][]config.Hook
}

func New(items map[string][]config.Hook) *Runner {
	copyItems := make(map[string][]config.Hook, len(items))
	for name, list := range items {
		copyItems[name] = append([]config.Hook(nil), list...)
	}
	return &Runner{items: copyItems}
}

func (r *Runner) Run(ctx context.Context, event Event) error {
	if r == nil {
		return nil
	}
	list := r.items[event.Name]
	if len(list) == 0 {
		return nil
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	for _, hook := range list {
		if err := runOne(ctx, hook, payload, event); err != nil {
			return err
		}
	}
	return nil
}

func hookError(result bashrun.Result) string {
	if result.TimedOut {
		return "timed out"
	}
	if result.Killed {
		return "killed"
	}
	if strings.TrimSpace(result.Output) != "" {
		return strings.TrimSpace(result.Output)
	}
	if result.Exit != "" {
		return result.Exit
	}
	return "unknown error"
}

func runOne(ctx context.Context, hook config.Hook, payload []byte, event Event) error {
	timeout := 10 * time.Second
	if hook.Timeout > 0 {
		timeout = time.Duration(hook.Timeout) * time.Second
	}
	if timeout > 2*time.Minute {
		timeout = 2 * time.Minute
	}
	hctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := hook.Command
	result := bashrun.Run(hctx, bashrun.Options{Command: command, Shell: hook.Shell, Dir: event.CWD, Timeout: timeout, Env: []string{"K_BRAIN_HOOK_EVENT=" + string(payload)}})
	if result.Exit != "" || result.TimedOut || result.Killed {
		return fmt.Errorf("hook %s failed: %s", event.Name, hookError(result))
	}
	if event.Name == PreToolUse {
		if reason, denied := deniedBy(result.Output); denied {
			return &DeniedError{Reason: reason}
		}
	}
	return nil
}

// deniedBy reads a structured PreToolUse reply. Output that is not such a JSON
// object, or whose decision is anything but "deny", allows the call, so hooks that
// print plain text keep working.
func deniedBy(output string) (string, bool) {
	output = strings.TrimSpace(output)
	candidates := []string{output}
	if i := strings.LastIndexByte(output, '\n'); i >= 0 {
		candidates = append(candidates, strings.TrimSpace(output[i+1:]))
	}
	for _, text := range candidates {
		if !strings.HasPrefix(text, "{") {
			continue
		}
		var reply struct {
			Decision string `json:"decision"`
			Reason   string `json:"reason"`
		}
		if json.Unmarshal([]byte(text), &reply) == nil {
			return strings.TrimSpace(reply.Reason), strings.EqualFold(reply.Decision, "deny")
		}
	}
	return "", false
}

func Parse(data []byte) (map[string][]config.Hook, error) {
	var hooks map[string][]config.Hook
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, nil
	}
	if err := json.Unmarshal(data, &hooks); err != nil {
		return nil, err
	}
	for name, list := range hooks {
		if strings.TrimSpace(name) == "" {
			return nil, errors.New("hook event name cannot be empty")
		}
		if !slices.Contains(config.HookEvents, name) {
			return nil, fmt.Errorf("unsupported hook event %q", name)
		}
		for i, h := range list {
			if strings.TrimSpace(h.Command) == "" {
				return nil, fmt.Errorf("hook %s[%d] command cannot be empty", name, i)
			}
		}
	}
	return hooks, nil
}
