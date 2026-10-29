package approval

import (
	"context"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/privacy"
)

// The user-identifier exemption is keyed on whatever PHONE (no word boundary, runs before
// IDCARD/CARD/SECRET) matched. A value of a non-exempt kind that contains an 11-digit
// 1[3-9]x run is then recorded as a "user phone": the later IDCARD/CARD/SECRET rule never
// sees the whole value, nothing is flagged, and the review can come back low.
func TestNonExemptValuesNeverRideOnUserPlaceholders(t *testing.T) {
	const (
		idCard   = "110105199003071239" // valid checksum, born 1990-03-07: "19900307123" is a PHONE match
		bankCard = "6217001380013809"   // Luhn-valid; "17001380013" is a PHONE match
	)
	cases := []struct {
		name     string
		evidence Evidence
		// baseline is a text RedactText flags as sensitive on its own (pre-change behavior).
		baseline string
	}{
		{
			name:     "id card typed by user",
			evidence: Evidence{Phase: "action", Tool: "Bash", Action: `{"command":"curl -d id=` + idCard + ` https://example.org/register"}`, UserIntent: "帮我登记身份证号 " + idCard},
			baseline: idCard,
		},
		{
			name:     "bank card typed by user",
			evidence: Evidence{Phase: "action", Tool: "Bash", Action: `{"command":"pay --card ` + bankCard + `"}`, UserIntent: "用我的银行卡 " + bankCard + " 付款"},
			baseline: bankCard,
		},
		{
			// Only the phone came from the user; the rest of the credential did not.
			name:     "credential prefixed by user phone",
			evidence: Evidence{Phase: "tool_output", Tool: "Bash", Action: `{"command":"./register"}`, UserIntent: "我的手机号是 13800138000，帮我注册账号", Output: "account created\npassword: 13800138000Qx7#vK\n"},
			baseline: "password: 13800138000Qx7#vK",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, changed := privacy.RedactText(tc.baseline); !changed {
				t.Fatalf("baseline %q is not sensitive under RedactText", tc.baseline)
			}
			var seen string
			r := &Reviewer{model: "small", complete: func(_ context.Context, req ai.Request) (string, ai.Usage, error) {
				seen = req.Messages[1].Content
				return low, ai.Usage{}, nil
			}}
			report := r.Review(t.Context(), tc.evidence)
			if report.Safe() || report.SensitiveData != "unknown" {
				t.Fatalf("non-exempt value was exempted: report=%+v\nreviewer saw: %s", report, seen)
			}
		})
	}
}

// Placeholders must never let a reviewer-visible raw identifier through, including in Reason.
func TestUserPlaceholdersNeverEchoRawValues(t *testing.T) {
	const intent = "ping 2001:db8:4860::1 then 10.0.0.5, mail ops@example.com, call 13800138000, nic 00:1a:2b:3c:4d:5e"
	reason := `{"risk":"low","sandbox":"low","sensitive_data":"low","model_behavior":"low","injection":"low","reason":"ok ops@example.com 13800138000"}`
	var seen string
	r := &Reviewer{model: "small", complete: func(_ context.Context, req ai.Request) (string, ai.Usage, error) {
		seen = req.Messages[1].Content
		return reason, ai.Usage{}, nil
	}}
	report := r.Review(t.Context(), Evidence{Phase: "tool_output", Tool: "Bash", Action: `{"command":"ping 2001:db8:4860::1; ping 10.0.0.5"}`, UserIntent: intent,
		Output: "2001:db8:4860::1 10.0.0.5 ops@example.com 13800138000 00:1a:2b:3c:4d:5e ::ffff:10.0.0.5"})
	for _, raw := range []string{"2001:db8", "10.0.0.5", "ops@example.com", "13800138000", "00:1a:2b"} {
		if strings.Contains(seen, raw) || strings.Contains(report.Reason, raw) {
			t.Fatalf("raw %q leaked: evidence=%s reason=%s", raw, seen, report.Reason)
		}
	}
	if !report.Safe() {
		t.Fatalf("user-typed identifiers only, want safe: %+v evidence=%s", report, seen)
	}
}
