package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

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
	// StopHookActive is set on a Stop event when an earlier Stop hook of the
	// same turn blocked the stop and the agent is continuing because of it.
	StopHookActive bool `json:"stopHookActive,omitempty"`
}

// MaxAdditionalContext bounds the additionalContext text one event delivers to
// the model, summed over its hooks.
const MaxAdditionalContext = 10 << 10

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

// Outcome is what the hooks of one event asked for beyond success or failure.
type Outcome struct {
	// Block is set when a Stop hook replied {"decision":"block"}; Reason is its
	// reason. Hooks after the blocking one do not run.
	Block  bool
	Reason string
	// AdditionalContext joins the additionalContext replies of UserPromptSubmit,
	// PostToolUse and PostToolUseFailure hooks, in order, capped at
	// MaxAdditionalContext bytes.
	AdditionalContext string
}

type hook struct {
	config.Hook
	match *regexp.Regexp
}

type Runner struct {
	items map[string][]hook
}

func New(items map[string][]config.Hook) *Runner {
	copyItems := make(map[string][]hook, len(items))
	for name, list := range items {
		for _, h := range list {
			// Configuration is validated when it loads. A matcher that still
			// fails to compile matches every tool, so a guard hook is never
			// silently skipped.
			match, _ := config.CompileHookMatcher(h.Matcher)
			copyItems[name] = append(copyItems[name], hook{Hook: h, match: match})
		}
	}
	return &Runner{items: copyItems}
}

// matches reports whether h runs for event: a matcher selects tool names on
// tool events and is ignored elsewhere.
func (h hook) matches(event Event) bool {
	if h.match == nil || !slices.Contains(config.HookToolEvents, event.Name) {
		return true
	}
	return h.match.MatchString(event.ToolName)
}

func (r *Runner) Run(ctx context.Context, event Event) error {
	_, err := r.RunOutcome(ctx, event)
	return err
}

// RunOutcome runs the hooks configured for event in order, stopping at the
// first failure, PreToolUse denial or Stop block. The outcome collected up to
// that point is returned with the error.
func (r *Runner) RunOutcome(ctx context.Context, event Event) (Outcome, error) {
	var outcome Outcome
	if r == nil {
		return outcome, nil
	}
	list := r.items[event.Name]
	if len(list) == 0 {
		return outcome, nil
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return outcome, err
	}
	var extra []string
	done := func(err error) (Outcome, error) {
		outcome.AdditionalContext = boundContext(strings.Join(extra, "\n\n"))
		return outcome, err
	}
	for _, h := range list {
		if !h.matches(event) {
			continue
		}
		output, err := runOne(ctx, h.Hook, payload, event)
		if err != nil {
			return done(err)
		}
		rep, ok := parseReply(output)
		if !ok {
			continue
		}
		switch event.Name {
		case PreToolUse:
			if strings.EqualFold(rep.Decision, "deny") {
				return done(&DeniedError{Reason: rep.Reason})
			}
		case Stop:
			if strings.EqualFold(rep.Decision, "block") {
				outcome.Block, outcome.Reason = true, rep.Reason
				return done(nil)
			}
		case UserPromptSubmit, PostToolUse, PostToolUseFailure:
			if text := strings.TrimSpace(rep.AdditionalContext); text != "" {
				extra = append(extra, text)
			}
		}
	}
	return done(nil)
}

// boundContext cuts text to MaxAdditionalContext bytes on a rune boundary.
func boundContext(text string) string {
	if len(text) <= MaxAdditionalContext {
		return text
	}
	const marker = "\n[additional context truncated]"
	cut := MaxAdditionalContext - len(marker)
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + marker
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

// runOne runs one hook with the event JSON in K_BRAIN_HOOK_EVENT and on stdin,
// and returns its output.
func runOne(ctx context.Context, hook config.Hook, payload []byte, event Event) (string, error) {
	timeout := 10 * time.Second
	if hook.Timeout > 0 {
		timeout = time.Duration(hook.Timeout) * time.Second
	}
	if timeout > 2*time.Minute {
		timeout = 2 * time.Minute
	}
	hctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result := bashrun.Run(hctx, bashrun.Options{
		Command: hook.Command,
		Shell:   hook.Shell,
		Dir:     event.CWD,
		Timeout: timeout,
		Env:     []string{"K_BRAIN_HOOK_EVENT=" + string(payload)},
		Stdin:   bytes.NewReader(payload),
	})
	if result.Exit != "" || result.TimedOut || result.Killed {
		return "", fmt.Errorf("hook %s failed: %s", event.Name, hookError(result))
	}
	return result.Output, nil
}

type reply struct {
	Decision          string `json:"decision"`
	Reason            string `json:"reason"`
	AdditionalContext string `json:"additionalContext"`
}

// parseReply reads a structured hook reply: a JSON object that is the whole
// output or its last line. Output that is not such an object is no reply, so
// hooks that print plain text keep working.
func parseReply(output string) (reply, bool) {
	output = strings.TrimSpace(output)
	candidates := []string{output}
	if i := strings.LastIndexByte(output, '\n'); i >= 0 {
		candidates = append(candidates, strings.TrimSpace(output[i+1:]))
	}
	for _, text := range candidates {
		if !strings.HasPrefix(text, "{") {
			continue
		}
		var rep reply
		if json.Unmarshal([]byte(text), &rep) == nil {
			rep.Reason = strings.TrimSpace(rep.Reason)
			return rep, true
		}
	}
	return reply{}, false
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
			if err := config.ValidateHook(name, i, h); err != nil {
				return nil, err
			}
		}
	}
	return hooks, nil
}
