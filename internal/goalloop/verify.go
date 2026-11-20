package goalloop

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

// DefaultVerifyMaxRejections is how many times the verifier may turn down a GOAL_MET
// claim for one goal before the next claim is accepted without another check, so a
// verifier that never agrees cannot keep a finished goal running forever.
const DefaultVerifyMaxRejections = 3

// VerifyEnabled reports whether a GOAL_MET claim is checked by an independent,
// hidden verification request before the goal completes. It is on unless the config
// turns it off.
func VerifyEnabled(cfg *config.Config) bool {
	return cfg == nil || cfg.GoalVerify == nil || *cfg.GoalVerify
}

// VerifyMaxRejections resolves the verifier rejection cap from the config.
func VerifyMaxRejections(cfg *config.Config) int {
	if cfg != nil && cfg.GoalVerifyMaxRejections > 0 {
		return cfg.GoalVerifyMaxRejections
	}
	return DefaultVerifyMaxRejections
}

// Verdict is the verifier's judgement of a GOAL_MET claim.
type Verdict struct {
	Met    bool   `json:"met"`
	Reason string `json:"reason"`
}

// VerifyInstructions is the system prompt of the verification request.
const VerifyInstructions = `You are an independent reviewer. An agent was given a goal and has just claimed it is accomplished (its reply began with ` + MetToken + `). You cannot run tools: judge only from the goal and the transcript excerpt you are given.

Be skeptical. Every part of the goal needs concrete evidence in the excerpt — commands that ran and passed, files that were written, behavior that was observed. If any part is missing, unverified, contradicted by a tool result, or only promised, the goal is NOT met.

Reply with ONLY one JSON object and nothing else — no prose, no code fence:
{"met": true or false, "reason": "one or two sentences; when not met, name exactly what is missing or what to do next"}`

// VerifyPrompt is the user message of the verification request: the goal, the
// excerpt of recent work and the reply that claimed the goal.
func VerifyPrompt(goal, transcript, claim string) string {
	var b strings.Builder
	b.WriteString("<goal>\n")
	b.WriteString(goal)
	b.WriteString("\n</goal>\n\n<recent_work>\n")
	b.WriteString(transcript)
	b.WriteString("\n</recent_work>\n\n<claim>\n")
	b.WriteString(claim)
	b.WriteString("\n</claim>\n\nIs the goal really met? Answer with the JSON object only.")
	return b.String()
}

// ParseVerdict reads the verifier's reply. It tolerates a code fence or text around
// the object, but the object must carry a boolean "met"; anything else is an error
// and the caller falls back to accepting the claim.
func ParseVerdict(reply string) (Verdict, error) {
	start, end := strings.Index(reply, "{"), strings.LastIndex(reply, "}")
	if start < 0 || end <= start {
		return Verdict{}, errors.New("verifier reply has no JSON object")
	}
	var raw struct {
		Met    *bool  `json:"met"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(reply[start:end+1]), &raw); err != nil {
		return Verdict{}, fmt.Errorf("verifier reply is not valid JSON: %w", err)
	}
	if raw.Met == nil {
		return Verdict{}, errors.New(`verifier reply has no boolean "met"`)
	}
	v := Verdict{Met: *raw.Met, Reason: strings.TrimSpace(raw.Reason)}
	if !v.Met && v.Reason == "" {
		v.Reason = "the verifier found the goal unmet but gave no reason"
	}
	return v, nil
}

// RejectedPrompt is the user turn submitted after the verifier turned down a GOAL_MET
// claim. It is a goal check (ContinueGoal recognizes it) with the verifier's reason
// in a clearly labelled section ahead of the usual round rules.
func RejectedPrompt(goal, reason string) string {
	return continueHead + goal + "\n\n" + fmt.Sprintf(`[goal verifier] An independent check of your %s claim found the goal is NOT met yet:
%s

Address this, verify it with your tools, and only then claim the goal again.

`, MetToken, strings.TrimSpace(reason)) + roundRules()
}
