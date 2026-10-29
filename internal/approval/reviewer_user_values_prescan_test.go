package approval

import (
	"context"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/privacy"
)

func captureReview() (*Reviewer, *string) {
	seen := new(string)
	return &Reviewer{model: "small", complete: func(_ context.Context, req ai.Request) (string, ai.Usage, error) {
		*seen = req.Messages[1].Content
		return low, ai.Usage{}, nil
	}}, seen
}

// The pre-scan runs on the original text, the exempting scan on text with placeholders.
// A placeholder ends in "]" / starts with "[", which creates a \b the original did not have,
// so IDCARD can match only in the second scan. That scan no longer checks the kind, so the
// ID card number is recorded and echoed as a user value.
func TestPlaceholderBoundaryNeverExemptsIDCard(t *testing.T) {
	const idCard = "110105200503071230" // valid checksum, born 2005: contains no PHONE match
	for name, joined := range map[string]string{
		"phone then id": "13800138000" + idCard,
		"id then phone": idCard + "13800138000",
		"email then id": "ops@example.com" + idCard,
	} {
		t.Run(name, func(t *testing.T) {
			r, seen := captureReview()
			report := r.Review(t.Context(), Evidence{Phase: "action", Tool: "Bash", Action: `{"command":"echo ` + joined + `"}`, UserIntent: "登记这个字段 " + joined})
			if report.Safe() || report.SensitiveData != "unknown" || strings.Contains(*seen, "[USER_IDCARD") {
				t.Fatalf("ID card exempted as a user value: report=%+v\nreviewer saw: %s", report, *seen)
			}
		})
	}
}

// The pre-scan only helps when some non-exempt rule recognizes the longer value. A credential
// that merely starts with the user's phone or landline, written in a form SECRET does not
// match (CLI flag with a space), still rides on the phone placeholder and is approved, though
// RedactText alone flags the same action.
func TestCredentialPrefixedByUserNumberNeedsHuman(t *testing.T) {
	for name, tc := range map[string]struct{ intent, value string }{
		"phone":    {"我的手机号是 13800138000，用它开数据库账号", "13800138000Qx7#vK"},
		"landline": {"公司座机 01088886666，用它开数据库账号", "01088886666Qx7#vK"},
	} {
		t.Run(name, func(t *testing.T) {
			action := `{"command":"mysql -u app --password ` + tc.value + ` -e 'select 1'"}`
			if _, changed := privacy.RedactText(action); !changed {
				t.Fatalf("baseline: RedactText does not flag %s", action)
			}
			r, seen := captureReview()
			report := r.Review(t.Context(), Evidence{Phase: "action", Tool: "Bash", Action: action, UserIntent: tc.intent})
			if report.Safe() || report.SensitiveData != "unknown" {
				t.Fatalf("credential suffix approved: report=%+v\nreviewer saw: %s", report, *seen)
			}
		})
	}
}

// Intent with a non-exempt value: the whole field is redacted, nothing is recorded, and every
// review in the run stays sensitive; no raw identifier reaches the reviewer.
func TestIntentWithNonExemptValueRecordsNothing(t *testing.T) {
	r, seen := captureReview()
	intent := "邮箱 ops@example.com，password: hunter2xyz，手机 13800138000"
	report := r.Review(t.Context(), Evidence{Phase: "tool_output", Tool: "Bash", Action: `{"command":"mail ops@example.com"}`, UserIntent: intent, Output: "sent to ops@example.com 13800138000"})
	if report.Safe() || report.SensitiveData != "unknown" || strings.Contains(*seen, "[USER_") {
		t.Fatalf("report=%+v evidence=%s", report, *seen)
	}
	for _, raw := range []string{"ops@example.com", "13800138000", "hunter2xyz"} {
		if strings.Contains(*seen, raw) {
			t.Fatalf("raw %q leaked: %s", raw, *seen)
		}
	}
}
