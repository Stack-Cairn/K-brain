package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func TestExitPlanModeOfferedOnlyInPlanModeWithAReviewer(t *testing.T) {
	prev := tools.PlanReview
	defer func() { tools.PlanReview = prev }()
	a := New(ai.New("http://localhost", "test"), "model1", 100, "system")

	tools.PlanReview = nil
	a.SetPlanMode(true)
	if slices.Contains(toolNames(a.AllTools()), exitPlanModeName) {
		t.Error("ExitPlanMode offered with nobody to review the plan")
	}
	tools.PlanReview = func(context.Context, string) (tools.PlanVerdict, bool) { return tools.PlanVerdict{}, false }
	if !slices.Contains(toolNames(a.AllTools()), exitPlanModeName) {
		t.Error("ExitPlanMode missing in plan mode with a reviewer")
	}
	a.SetPlanMode(false)
	if slices.Contains(toolNames(a.AllTools()), exitPlanModeName) {
		t.Error("ExitPlanMode offered outside plan mode")
	}
}

// planServer answers every request with an ExitPlanMode call until the last
// message is a tool result, then with text; it records the tool results it
// saw and the tools it was offered.
type planServer struct {
	mu      sync.Mutex
	results []string
	offered [][]string
}

func (p *planServer) handler(w http.ResponseWriter, r *http.Request) {
	var req ai.Request
	_ = json.NewDecoder(r.Body).Decode(&req)
	w.Header().Set("Content-Type", "text/event-stream")
	p.mu.Lock()
	var names []string
	for _, t := range req.Tools {
		names = append(names, t.Function.Name)
	}
	p.offered = append(p.offered, names)
	last := req.Messages[len(req.Messages)-1]
	if last.Role == "tool" {
		p.results = append(p.results, last.Content)
	}
	n := len(p.results)
	p.mu.Unlock()
	if last.Role == "tool" && !strings.Contains(last.Content, "asked for changes") {
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"implementing"},"finish_reason":"stop"}]}`+"\n\n")
	} else {
		args, _ := json.Marshal(map[string]string{"plan": fmt.Sprintf("# Plan v%d\n1. Do it", n+1)})
		call, _ := json.Marshal(string(args))
		fmt.Fprintf(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"p%d","type":"function","function":{"name":"ExitPlanMode","arguments":%s}}]}}]}`+"\n\n", n, call)
		fmt.Fprint(w, `data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n")
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func TestExitPlanModeVerdicts(t *testing.T) {
	prev := tools.PlanReview
	defer func() { tools.PlanReview = prev }()

	for _, tc := range []struct {
		name     string
		verdicts []func(a *Agent) (tools.PlanVerdict, bool)
		requests int
		want     []string
	}{
		{"approve continues the turn out of plan mode", []func(a *Agent) (tools.PlanVerdict, bool){
			func(a *Agent) (tools.PlanVerdict, bool) {
				a.SetPlanMode(false)
				return tools.PlanVerdict{Approved: true}, true
			},
		}, 2, []string{"approved the plan"}},
		{"changes revise the plan in the same turn", []func(a *Agent) (tools.PlanVerdict, bool){
			func(*Agent) (tools.PlanVerdict, bool) { return tools.PlanVerdict{Feedback: "line 2: use a flag"}, true },
			func(*Agent) (tools.PlanVerdict, bool) { return tools.PlanVerdict{}, false },
		}, 2, []string{"asked for changes", "closed the plan review"}},
		{"closing the review ends the turn", []func(a *Agent) (tools.PlanVerdict, bool){
			func(*Agent) (tools.PlanVerdict, bool) { return tools.PlanVerdict{}, false },
		}, 1, []string{"closed the plan review"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ps := &planServer{}
			srv := httptest.NewServer(http.HandlerFunc(ps.handler))
			defer srv.Close()
			a := New(ai.New(srv.URL, "k"), "m", 100, "sys")
			a.SetPlanMode(true)
			var plans []string
			tools.PlanReview = func(_ context.Context, plan string) (tools.PlanVerdict, bool) {
				plans = append(plans, plan)
				return tc.verdicts[len(plans)-1](a)
			}
			if _, err := a.Turn(t.Context(), "plan it", Events{}); err != nil {
				t.Fatal(err)
			}
			ps.mu.Lock()
			defer ps.mu.Unlock()
			if len(plans) != len(tc.verdicts) {
				t.Fatalf("reviewed %d plans, want %d", len(plans), len(tc.verdicts))
			}
			if len(ps.offered) != tc.requests {
				t.Fatalf("%d model requests, want %d", len(ps.offered), tc.requests)
			}
			// The tool results the model saw, in order.
			var seen []string
			for _, m := range a.MessagesSnapshot() {
				if m.Role == "tool" {
					seen = append(seen, m.Content)
				}
			}
			if len(seen) != len(tc.want) {
				t.Fatalf("tool results %q, want %d", seen, len(tc.want))
			}
			for i, w := range tc.want {
				if !strings.Contains(seen[i], w) {
					t.Errorf("result %d = %q, want it to mention %q", i, seen[i], w)
				}
			}
			if strings.Contains(tc.name, "changes") && !strings.Contains(seen[0], "line 2: use a flag") {
				t.Errorf("feedback not passed to the model: %q", seen[0])
			}
			if strings.HasPrefix(tc.name, "approve") && slices.Contains(ps.offered[1], exitPlanModeName) {
				t.Error("ExitPlanMode still offered after the plan was approved")
			}
		})
	}
}
