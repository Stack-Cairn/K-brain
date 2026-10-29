package approval

import (
	"context"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/privacy"
)

// Sweep: a user-typed exempt identifier joined to a non-exempt value by every byte the
// joinedUserValue whitelist accepts (and none), typed in the intent and echoed by the action.
// Anything recognized on origin/main as a non-exempt kind must still force manual review.
func TestJoinedSweepKeepsNonExemptKindsSensitive(t *testing.T) {
	users := []string{"13800138000", "010-88886666", "ops@example.com"}
	others := []string{
		"110105199003071239",        // IDCARD, 1990 (contains a PHONE match)
		"110105200503071230",        // IDCARD, 2005
		"6217001380013809",          // CARD (contains a PHONE match)
		"4111111111111111",          // CARD
		"DE89370400440532013000",    // IBAN
		"token=Qx7vK9#a",            // SECRET
		"sk-abcdefghijklmnopqrstuv", // API_KEY
		"AKIAABCDEFGHIJKLMNOP",      // ACCESS_KEY
	}
	seps := []string{"", " ", "\t", "(", "[", "{", "<", `"`, "'", "`", "=", ":", ",", ";", "/", "]", ")", "}", ">", ".", "!", "?", `\`, ": ", "，", "的"}
	r := &Reviewer{model: "small", complete: func(context.Context, ai.Request) (string, ai.Usage, error) { return low, ai.Usage{}, nil }}
	checked := 0
	for _, u := range users {
		for _, o := range others {
			for _, s := range seps {
				for _, joined := range []string{u + s + o, o + s + u} {
					if _, changed := privacy.RedactText(joined); !changed {
						continue
					}
					checked++
					report := r.Review(t.Context(), Evidence{Phase: "action", Tool: "Bash", Action: `{"command":"echo ` + strings.ReplaceAll(joined, `"`, `\"`) + `"}`, UserIntent: "记录 " + joined})
					if report.Safe() {
						t.Errorf("approved %q", joined)
					}
				}
			}
		}
	}
	t.Logf("checked %d joined inputs", checked)
}

// A value the user did not type must never borrow a user placeholder, however close it is
// to one the user did type.
func TestNearMissValuesStaySensitive(t *testing.T) {
	const intent = "联系 13800138000 / 010-88886666 / ops@example.com，连 10.0.0.5 和 2001:db8:4860::1，网卡 00:1a:2b:3c:4d:5e"
	r := &Reviewer{model: "small", complete: func(context.Context, ai.Request) (string, ai.Usage, error) { return low, ai.Usage{}, nil }}
	for _, output := range []string{
		"+86 13800138000", "8613800138000", "138 0013 8000", "138-0013-8000", "13800138001",
		"010 88886666", "(010)88886666", "010-88886667", "010-88886666转123",
		"OPS@example.com", "ops@example.com.cn", "ops@example.co", "xops@example.com", "ops+1@example.com",
		"10.0.0.50", "110.0.0.5", "64:ff9b::10.0.0.5", "2001:db8::10.0.0.5",
		"2001:db8:4860::1:2", "2001:db8:4860::10", "2001:DB8:4860::1", "2001:db8:4860:0::1",
		"00:1a:2b:3c:4d:5f", "00:1A:2B:3C:4D:5E", "00-1a-2b-3c-4d-5e",
	} {
		if _, changed := privacy.RedactText(output); !changed {
			continue
		}
		report := r.Review(t.Context(), Evidence{Phase: "tool_output", Tool: "Bash", Action: `{"command":"cat log"}`, UserIntent: intent, Output: "got " + output + " ok"})
		if report.Safe() {
			t.Errorf("non-user value %q approved", output)
		}
	}
}
