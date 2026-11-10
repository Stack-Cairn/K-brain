// Package goalloop holds the session-goal rules shared by the TUI and the backend:
// the round prompts, the GOAL_MET completion and GOAL_NEEDS_INPUT checks, and the
// round limit resolution.
package goalloop

import (
	"fmt"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

// MetToken is the literal a reply must lead with to declare the goal accomplished.
const MetToken = "GOAL_MET"

// NeedsInputToken is the literal a reply leads with when the goal cannot advance
// without the user's answer; the goal then pauses until the user replies.
const NeedsInputToken = "GOAL_NEEDS_INPUT"

// MaxRounds resolves the continuation limit: a project override for dir wins over the
// global config value, which wins over the built-in default.
func MaxRounds(dir string, global int) int {
	if dir != "" {
		if n := config.ProjectGoalMaxRounds(dir); n > 0 {
			return n
		}
	}
	if global > 0 {
		return global
	}
	return config.DefaultGoalMaxRounds
}

const continueHead = "[goal check] The session goal is:\n"

// ContinuePrompt is the user turn submitted after every unfinished round.
func ContinuePrompt(goal string) string {
	return continueHead + goal + "\n\n" + roundRules()
}

// ContinueGoal reports whether prompt is a ContinuePrompt and, if so, its goal.
func ContinueGoal(prompt string) (string, bool) {
	rest, ok := strings.CutPrefix(prompt, continueHead)
	if !ok {
		return "", false
	}
	goal, _, _ := strings.Cut(rest, "\n\n")
	return goal, true
}

// StartPrompt is the request-only instruction of the round that sets a goal. It carries
// the same rules as later rounds, so even the first round can finish the goal or ask
// for the user's input.
func StartPrompt(goal string) string {
	return fmt.Sprintf("The user set this session goal with /goal. Work toward it now:\n%s\n\n%s", goal, roundRules())
}

// ReplyPrompt is the request-only instruction of the round that carries the user's
// answer to a goal that paused for input; the visible user turn is the answer itself.
func ReplyPrompt(goal string) string {
	return fmt.Sprintf(`[goal check] The user just replied to your request for input on the session goal:
%s

Use their reply. If it changes the goal or tells you to stop, do not continue on your own: say what you understood and begin your reply with the literal token %s so the goal waits for them.

%s`, goal, NeedsInputToken, roundRules())
}

// PausedNotice is the request-only text of a plain user turn while the goal is paused
// by the user (or restored paused). The goal stays visible in history, so without it
// the model tends to pick the goal back up on its own.
func PausedNotice(goal string) string {
	return fmt.Sprintf(`[goal paused] This session goal is paused:
%s

Do not continue working on it in this turn unless the user's message asks you to; answer the user's message. Do not use the %s or %s tokens — the goal resumes only when the user resumes it.`, goal, MetToken, NeedsInputToken)
}

// ClearedNotice is the request-only text of a plain user turn after the user cleared
// the goal that history still shows.
func ClearedNotice(goal string) string {
	return fmt.Sprintf(`[goal cleared] The user cleared this session goal:
%s

Do not continue working on it unless the user's message asks you to; answer the user's message. Do not use the %s or %s tokens.`, goal, MetToken, NeedsInputToken)
}

func roundRules() string {
	return fmt.Sprintf(`If the goal is FULLY accomplished and you have VERIFIED it with your tools (builds pass, tests pass, behavior confirmed), your reply MUST begin with the literal token %s as the very first characters — no preamble, no "Verified:" line, nothing before it. Example first line: "%s — shipped and verified."

If you cannot make progress without an answer only the user can give (a decision, missing information, or an approval the goal requires), ask for it and begin your reply with the literal token %s as the very first characters. The goal then waits for the user's reply. Do not use it for questions you can settle yourself with reasonable assumptions.

Otherwise use neither %s nor %s — keep working toward the goal right now with your tools. If any part is incomplete, unverified, or you are unsure, that means keep working.`, MetToken, MetToken, NeedsInputToken, MetToken, NeedsInputToken)
}

// Met reports whether a final reply declares the goal accomplished. As the continuation
// prompt demands, the reply must lead with the token: only whitespace and Markdown
// emphasis, heading, quote or code markers may precede it, and it must end at a word
// boundary. A mention later in the reply ("not GOAL_MET yet") never counts.
func Met(final string) bool { return leads(final, MetToken) }

// NeedsInput reports whether a final reply asks the user for input the goal cannot
// advance without. It follows the same leading-token rule as Met.
func NeedsInput(final string) bool { return leads(final, NeedsInputToken) }

func leads(final, token string) bool {
	head := strings.TrimLeft(final, " \t\r\n*_#>`~")
	if !strings.HasPrefix(head, token) {
		return false
	}
	if rest := head[len(token):]; rest != "" {
		c := rest[0]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' {
			return false
		}
	}
	return true
}
