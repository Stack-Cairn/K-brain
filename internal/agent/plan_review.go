package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// exitPlanModeName is the tool that submits a plan for review. The backend
// defines its own for /v1 runs (approval happens out of band there); this one
// is for clients that review the plan in-process through tools.PlanReview.
const exitPlanModeName = "ExitPlanMode"

// offersPlanReview reports whether the agent's own ExitPlanMode is offered:
// only in plan mode, and only when a client can review the plan.
func (a *Agent) offersPlanReview() bool { return a.PlanMode() && tools.PlanReview != nil }

// exitPlanTool submits the plan to the user. Approval leaves plan mode and the
// turn goes on to implement it; requested changes keep plan mode and the turn
// goes on to revise the plan; closing the review ends the turn.
func exitPlanTool(a *Agent) tools.Tool {
	return tools.Tool{
		Def: ai.NewTool(exitPlanModeName,
			"Submit the complete markdown plan for the user's review when planning is done. The user approves it (plan mode ends and you implement it right away), asks for changes (revise the plan and submit it again), or closes the review (end your turn).",
			`{"type":"object","additionalProperties":false,"properties":{"plan":{"type":"string","description":"The complete plan in markdown"}},"required":["plan"]}`),
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args struct {
				Plan string `json:"plan"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", err
			}
			if strings.TrimSpace(args.Plan) == "" {
				return "", errors.New("plan is required")
			}
			review := tools.PlanReview
			if review == nil {
				return "", errors.New("no interactive user to review the plan; present it as text and end your turn")
			}
			verdict, ok := review(ctx, args.Plan)
			if err := ctx.Err(); err != nil {
				return "", err
			}
			switch {
			case !ok:
				return "The user closed the plan review without deciding. Plan mode stays on; end your turn and wait for their reply.", nil
			case verdict.Approved:
				return "The user approved the plan and plan mode is off. Implement the plan now, step by step, and verify the result.", nil
			}
			a.planRevise.Store(true)
			feedback := strings.TrimSpace(verdict.Feedback)
			if feedback == "" {
				feedback = "(no comment)"
			}
			return "The user asked for changes to the plan:\n\n" + feedback + "\n\nRevise the plan accordingly and submit the complete revised plan again with ExitPlanMode.", nil
		},
	}
}
