package approval

import (
	"context"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/privacy"
)

func captureJoinedReview() (*Reviewer, *string) {
	seen := new(string)
	return &Reviewer{model: "small", complete: func(_ context.Context, req ai.Request) (string, ai.Usage, error) {
		*seen = req.Messages[1].Content
		return low, ai.Usage{}, nil
	}}, seen
}

// gluedNumber only looks at [0-9A-Za-z_] and only at PHONE/LANDLINE. A credential joined to a
// user identifier by a symbol (user:pass for curl -u, phone#suffix) still rides on the
// placeholder; RedactText alone flags every one of these actions.
func TestSymbolJoinedCredentialNextToUserIdentifier(t *testing.T) {
	for name, tc := range map[string]struct{ intent, action string }{
		"curl basic auth with user email": {"用 ops@example.com 这个账号调一下 API", `{"command":"curl -u ops@example.com:Qx7#vK https://api.example.org/v1/me"}`},
		"curl basic auth with user phone": {"我的手机号 13800138000，帮我登录", `{"command":"curl -u 13800138000:Qx7#vK https://api.example.org/login"}`},
		"phone then symbol then secret":   {"我的手机号是 13800138000，用它开数据库账号", `{"command":"mysql -u app --password 13800138000#Qx7vK -e 'select 1'"}`},
		"secret then symbol then phone":   {"我的手机号是 13800138000，用它开数据库账号", `{"command":"mysql -u app --password Qx7v#13800138000 -e 'select 1'"}`},
		"dashed phone then dash secret":   {"手机 138-0013-8000", `{"command":"mysql -u app --password 138-0013-8000-Qx7vK -e 'select 1'"}`},
	} {
		t.Run(name, func(t *testing.T) {
			if _, changed := privacy.RedactText(tc.action); !changed {
				t.Fatalf("baseline: RedactText does not flag %s", tc.action)
			}
			r, seen := captureJoinedReview()
			report := r.Review(t.Context(), Evidence{Phase: "action", Tool: "Bash", Action: tc.action, UserIntent: tc.intent})
			if report.Safe() || report.SensitiveData != "unknown" {
				t.Fatalf("credential next to user identifier approved: report=%+v\nreviewer saw: %s", report, *seen)
			}
		})
	}
}

// A glued intent rolls back the values it added: a later field repeating the bare number
// must not receive a user placeholder, and the run stays sensitive.
func TestJoinedIntentDoesNotSeedLaterFields(t *testing.T) {
	r, seen := captureJoinedReview()
	report := r.Review(t.Context(), Evidence{Phase: "tool_output", Tool: "Bash",
		Action:     `{"command":"sms 13800138000 ops@example.com"}`,
		UserIntent: "邮箱 ops@example.com，手机 13800138000，口令 13800138000Qx7",
		Output:     "sent to 13800138000 / ops@example.com"})
	if report.Safe() || report.SensitiveData != "unknown" || strings.Contains(*seen, "[USER_") {
		t.Fatalf("report=%+v evidence=%s", report, *seen)
	}
	for _, raw := range []string{"13800138000", "ops@example.com"} {
		if strings.Contains(*seen, raw) {
			t.Fatalf("raw %q leaked: %s", raw, *seen)
		}
	}
}

// Natural-language neighbours must keep the exemption (otherwise the feature is moot), and
// Unicode letters/digits are outside the glued class by design.
func TestUnicodeNeighboursKeepExemption(t *testing.T) {
	for name, tc := range map[string]struct{ intent, output string }{
		"cjk around phone":  {"给13800138000发短信", "已发送给13800138000。"},
		"cjk around email":  {"发邮件到 ops@example.com 谢谢", "已投递 ops@example.com，完成"},
		"spaced phone kept": {"手机 138 0013 8000", "sent: 138 0013 8000"},
	} {
		t.Run(name, func(t *testing.T) {
			r, seen := captureJoinedReview()
			report := r.Review(t.Context(), Evidence{Phase: "tool_output", Tool: "Bash", Action: `{"command":"notify"}`, UserIntent: tc.intent, Output: tc.output})
			if !report.Safe() || strings.Contains(*seen, "138") || strings.Contains(*seen, "ops@") {
				t.Fatalf("report=%+v evidence=%s", report, *seen)
			}
		})
	}
}
