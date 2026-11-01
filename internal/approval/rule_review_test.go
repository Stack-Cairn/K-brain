package approval

import (
	"context"
	"testing"
)

func TestRuleStandsInForReview(t *testing.T) {
	cautious := Report{Risk: "medium", Sandbox: "low", SensitiveData: "low", ModelBehavior: "low", Injection: "low"}
	for _, tc := range []struct {
		name       string
		report     *Report
		ruleAllows bool
		want       bool
	}{
		{"cautious review with a rule", &cautious, true, true},
		{"cautious review without a rule", &cautious, false, false},
		{"no review", nil, true, false},
		{"high risk", &Report{Risk: "high", Sandbox: "low", SensitiveData: "low", ModelBehavior: "low", Injection: "low"}, true, false},
		{"unknown risk", &Report{Risk: "unknown", Sandbox: "low", SensitiveData: "low", ModelBehavior: "low", Injection: "low"}, true, false},
		{"tainted by earlier output", &Report{Risk: "medium", Sandbox: "low", SensitiveData: "low", ModelBehavior: "low", Injection: "unknown"}, true, false},
		{"sensitive data", &Report{Risk: "medium", Sandbox: "low", SensitiveData: "medium", ModelBehavior: "low", Injection: "low"}, true, false},
		{"sandbox concern", &Report{Risk: "medium", Sandbox: "medium", SensitiveData: "low", ModelBehavior: "low", Injection: "low"}, true, false},
		{"model behaviour", &Report{Risk: "medium", Sandbox: "low", SensitiveData: "low", ModelBehavior: "high", Injection: "low"}, true, false},
	} {
		ctx := t.Context()
		if tc.report != nil {
			ctx = context.WithValue(ctx, ActionReportKey{}, *tc.report)
		}
		if got := RuleStandsInForReview(ctx, tc.ruleAllows); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestReviewLimitsOnRulesAndReadOnly(t *testing.T) {
	safe := Report{Risk: "low", Sandbox: "low", SensitiveData: "low", ModelBehavior: "low", Injection: "low"}
	cautious := Report{Risk: "medium", Sandbox: "low", SensitiveData: "low", ModelBehavior: "low", Injection: "low"}
	concrete := Report{Risk: "medium", Sandbox: "low", SensitiveData: "high", ModelBehavior: "low", Injection: "low"}
	for _, tc := range []struct {
		name               string
		report             *Report
		readOnly, remember bool
	}{
		{"no review", nil, true, true},
		{"safe review", &safe, true, true},
		{"cautious review", &cautious, false, true},
		{"concrete finding", &concrete, false, false},
	} {
		ctx := t.Context()
		if tc.report != nil {
			ctx = context.WithValue(ctx, ActionReportKey{}, *tc.report)
		}
		if got := ReadOnlyUsable(ctx); got != tc.readOnly {
			t.Errorf("%s: ReadOnlyUsable=%v", tc.name, got)
		}
		if got := RulesUsable(ctx); got != tc.remember {
			t.Errorf("%s: RulesUsable=%v", tc.name, got)
		}
	}
}
