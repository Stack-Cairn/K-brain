package config

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// HookToolEvents are the hook events that carry a tool name and accept a matcher.
var HookToolEvents = []string{"PreToolUse", "PostToolUse", "PostToolUseFailure"}

// CompileHookMatcher compiles a hook matcher. An empty matcher or "*" matches
// every tool and yields nil. Anything else is a regular expression that must
// match the whole tool name, so "bash" matches only bash and "edit|write"
// matches either.
func CompileHookMatcher(matcher string) (*regexp.Regexp, error) {
	matcher = strings.TrimSpace(matcher)
	if matcher == "" || matcher == "*" {
		return nil, nil
	}
	return regexp.Compile("^(?:" + matcher + ")$")
}

// ValidateHook checks entry i of the hooks configured for event.
func ValidateHook(event string, i int, hook Hook) error {
	if strings.TrimSpace(hook.Command) == "" {
		return fmt.Errorf("hook %s[%d] command cannot be empty", event, i)
	}
	if hook.Timeout < 0 {
		return fmt.Errorf("hook %s[%d] timeout must be non-negative", event, i)
	}
	if strings.TrimSpace(hook.Matcher) != "" && !slices.Contains(HookToolEvents, event) {
		return fmt.Errorf("hook %s[%d] matcher is only supported for %s", event, i, strings.Join(HookToolEvents, ", "))
	}
	if _, err := CompileHookMatcher(hook.Matcher); err != nil {
		return fmt.Errorf("hook %s[%d] matcher %q is invalid: %v", event, i, hook.Matcher, err)
	}
	return nil
}
