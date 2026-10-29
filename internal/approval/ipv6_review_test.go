package approval

import (
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// Review regression for bc73e7a: in auto review mode an SPF lookup that prints
// a real IPv6 prefix must still force sensitive_data=unknown and be withheld.
// Before bc73e7a "ip6:2001:db8:4860::/32" was redacted; now it passes through.
func TestReviewSPFRecordIPv6StillForcesManualReview(t *testing.T) {
	output := strings.Join([]string{
		"ManagedProcess start",
		"status=exited",
		"exit_code=0",
		`example.com. 300 IN TXT "v=spf1 ip6:2001:db8:4860::/32 include:_spf.example.com ~all"`,
	}, "\n")
	report := fake(low).Review(t.Context(), Evidence{Tool: "ManagedProcess", Action: `{"action":"start","command":"dig TXT example.com"}`, Output: output})
	if report.Safe() || report.SensitiveData != "unknown" {
		t.Fatalf("IPv6 in SPF record not treated as sensitive: %+v", report)
	}
	run := NewRun("auto", "check dns", fake(low), func(string, string, Report, bool) {})
	h := run.Hooks(func(tools.GateRequest) (tools.GateDecision, string) { return tools.GateAllowOnce, "" })
	if result := h.After(t.Context(), "ManagedProcess", nil, tools.Result{Text: output}); result.Text == output {
		t.Fatalf("output with IPv6 released without review: %q", result.Text)
	}
}
