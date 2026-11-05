package approval

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// Asking the user has nothing for a reviewer to judge: AskUserQuestion and question
// skip the action and output reviews in every review mode, so the question is shown
// at once instead of after up to Timeout (or after a manual prompt when the reviewer's
// reply is unreadable). The gate still decides the call, so deny rules keep winning.
func TestInteractionToolsSkipReview(t *testing.T) {
	for _, mode := range []string{"auto", "risk_only"} {
		for _, name := range []string{"AskUserQuestion", "question"} {
			t.Run(mode+"/"+name, func(t *testing.T) {
				var reviews, published, gates atomic.Int32
				reviewer := &Reviewer{model: "small", complete: func(context.Context, ai.Request) (string, ai.Usage, error) {
					reviews.Add(1)
					return "unreadable", ai.Usage{}, nil
				}}
				run := NewRun(mode, "ask me", reviewer, func(string, string, Report, bool) { published.Add(1) })
				var gated tools.GateRequest
				deny := false
				hooks := run.Hooks(func(req tools.GateRequest) (tools.GateDecision, string) {
					gates.Add(1)
					gated = req
					if deny {
						return tools.GateReject, "denied by permission rule " + name
					}
					return tools.GateAllowOnce, ""
				})
				ran := 0
				catalog := []tools.Tool{{Def: ai.NewTool(name, "ask", `{"type":"object"}`), Run: func(ctx context.Context, _ json.RawMessage) (string, error) {
					ran++
					if _, reviewed := ctx.Value(ActionReportKey{}).(Report); reviewed {
						t.Error("interaction tool ran with a review report")
					}
					return "user picked Fast", nil
				}}}
				ctx := tools.WithReview(context.Background(), hooks)
				result := tools.ExecuteResult(ctx, catalog, name, json.RawMessage(`{"question":"Which?"}`), false)
				if result.Failed || result.Text != "user picked Fast" || ran != 1 {
					t.Fatalf("result=%+v ran=%d", result, ran)
				}
				if reviews.Load() != 0 || published.Load() != 0 {
					t.Fatalf("reviewed %d times, published %d reports", reviews.Load(), published.Load())
				}
				if mode == "auto" && (gates.Load() != 1 || gated.Tool != name) {
					t.Fatalf("gate calls=%d request=%+v", gates.Load(), gated)
				}
				if mode == "risk_only" && gates.Load() != 0 {
					t.Fatalf("risk_only gated the call %d times", gates.Load())
				}
				if mode != "auto" {
					return
				}
				deny = true
				result = tools.ExecuteResult(ctx, catalog, name, json.RawMessage(`{"question":"Which?"}`), false)
				if !result.Failed || !strings.Contains(result.Text, "denied by permission rule") || ran != 1 {
					t.Fatalf("deny did not win: result=%+v ran=%d", result, ran)
				}
				if reviews.Load() != 0 {
					t.Fatal("denied call was reviewed")
				}
			})
		}
	}
}

// Every other tool, including ExitPlanMode, is still reviewed.
func TestNonInteractionToolsStillReviewed(t *testing.T) {
	for _, name := range []string{"ExitPlanMode", "Read", "todowrite"} {
		var reviews atomic.Int32
		reviewer := &Reviewer{model: "small", complete: func(context.Context, ai.Request) (string, ai.Usage, error) {
			reviews.Add(1)
			return low, ai.Usage{}, nil
		}}
		run := NewRun("auto", "plan", reviewer, func(string, string, Report, bool) {})
		ctx := tools.WithReview(context.Background(), run.Hooks(func(tools.GateRequest) (tools.GateDecision, string) { return tools.GateAllowOnce, "" }))
		catalog := []tools.Tool{{Def: ai.NewTool(name, "x", `{"type":"object"}`), Run: func(context.Context, json.RawMessage) (string, error) { return "ok", nil }}}
		tools.ExecuteResult(ctx, catalog, name, json.RawMessage(`{}`), false)
		if reviews.Load() != 2 {
			t.Fatalf("%s: %d reviews, want action and output", name, reviews.Load())
		}
	}
}
