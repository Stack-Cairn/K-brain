package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/tools/bashrun"
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
		if err := runOne(ctx, hook, payload, event.Name); err != nil {
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

func runOne(ctx context.Context, hook config.Hook, payload []byte, name string) error {
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
	result := bashrun.Run(hctx, bashrun.Options{Command: command, Shell: hook.Shell, Timeout: timeout, Env: []string{"K_BRAIN_HOOK_EVENT=" + string(payload)}})
	if result.Exit != "" || result.TimedOut || result.Killed {
		return fmt.Errorf("hook %s failed: %s", name, hookError(result))
	}
	return nil
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
		switch name {
		case "SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "Stop":
		default:
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
