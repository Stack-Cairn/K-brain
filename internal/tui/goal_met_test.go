package tui

import (
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func TestGoalMet(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"prefix exact", "GOAL_MET — done", true},
		{"prefix with leading space", "  GOAL_MET — done", true},
		{"token after one-line preamble (the observed loop bug)", "Verified: all green.\n\nGOAL_MET — shipped", true},
		{"token mid-paragraph", "The work is done. GOAL_MET. Summary follows.", true},
		{"aspirational mention stays false", "I am making progress toward GOAL_MET soon", false},
		{"token as substring of word", "GOAL_METRICS look good", false},
		{"no token", "still working on it", false},
		{"token only deep in a long reply", strings.Repeat("x", 500) + " GOAL_MET", false},
		{"similar but wrong token", "GOALMET — done", false},
		{"empty", "", false},
	}
	for _, c := range cases {
		if got := goalMet(c.in); got != c.want {
			t.Errorf("%s: goalMet(%q) = %v, want %v", c.name, c.in[:min(30, len(c.in))], got, c.want)
		}
	}
}

func TestGoalLoopEndsOnMidMessageToken(t *testing.T) {
	m := &model{goal: "ship the thing", goalRounds: 1}
	m.cfg = nil
	tm, cmd := m.Update(turnDoneMsg{final: "Verified: all checks pass.\n\nGOAL_MET — shipped and verified."})
	mm := tm.(*model)
	if mm.goal != "" {
		t.Fatalf("goal must clear on GOAL_MET, still %q", mm.goal)
	}
	if cmd != nil {
		t.Fatal("no continuation turn must be submitted")
	}
}

func TestGoalLoopContinuesWithoutToken(t *testing.T) {
	m := goalFromContextModel(t, 200, `{"choices":[{"message":{"content":"ok"}}]}`)
	m.goal = "ship the thing"
	m.goalRounds = 0
	_, cmd := m.Update(turnDoneMsg{final: "still working"})
	if cmd == nil {
		t.Fatal("goal loop must continue when the token is absent")
	}
}

func TestGoalLoopDoesNotCompleteOnTruncatedResponse(t *testing.T) {
	m := goalFromContextModel(t, 200, `{"choices":[{"message":{"content":"ok"}}]}`)
	m.goal = "ship the thing"
	m.goalRounds = 0
	_, cmd := m.Update(turnDoneMsg{final: "GOAL_MET — done\n[response truncated by output limit]", stopReason: ai.StopReasonLength})
	if cmd == nil || m.goal == "" {
		t.Fatal("truncated completion marker ended the goal")
	}
}
