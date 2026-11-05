package tools

import "context"

// PlanVerdict is the user's answer to a plan the agent submitted with
// ExitPlanMode.
type PlanVerdict struct {
	// Approved means the user accepted the plan; the client has already left
	// plan mode, so the agent goes on to implement it in the same turn.
	Approved bool
	// Feedback is what the user wants changed when they did not approve: a
	// general comment and comments on single lines of the plan.
	Feedback string
}

// PlanReview shows a submitted plan to the interactive user and waits for
// their verdict; ok is false when they closed the review without deciding. It
// is nil where nobody can review a plan in-process (the backend, ACP), and
// the agent then does not offer its ExitPlanMode tool.
var PlanReview func(ctx context.Context, plan string) (verdict PlanVerdict, ok bool)
