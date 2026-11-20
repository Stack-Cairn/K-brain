package goalloop

import (
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

func TestParseVerdict(t *testing.T) {
	cases := []struct {
		name, in string
		met      bool
		reason   string
		err      bool
	}{
		{"plain met", `{"met":true,"reason":"tests pass"}`, true, "tests pass", false},
		{"fenced not met", "```json\n{\"met\": false, \"reason\": \"no build\"}\n```", false, "no build", false},
		{"prose around", "Verdict: {\"met\":false,\"reason\":\"x\"} done", false, "x", false},
		{"missing reason when unmet", `{"met":false}`, false, "the verifier found the goal unmet but gave no reason", false},
		{"no object", "looks good", false, "", true},
		{"no met field", `{"reason":"x"}`, false, "", true},
		{"met not bool", `{"met":"yes"}`, false, "", true},
		{"broken json", `{"met":true,`, false, "", true},
	}
	for _, c := range cases {
		v, err := ParseVerdict(c.in)
		if (err != nil) != c.err {
			t.Fatalf("%s: err %v", c.name, err)
		}
		if err == nil && (v.Met != c.met || v.Reason != c.reason) {
			t.Fatalf("%s: got %+v", c.name, v)
		}
	}
}

func TestRejectedPromptIsAGoalCheckWithTheReason(t *testing.T) {
	p := RejectedPrompt("ship it", "tests were not run")
	goal, ok := ContinueGoal(p)
	if !ok || goal != "ship it" {
		t.Fatalf("ContinueGoal(%q) = %q %v", p, goal, ok)
	}
	for _, want := range []string{"[goal verifier]", "tests were not run", MetToken, NeedsInputToken} {
		if !strings.Contains(p, want) {
			t.Fatalf("prompt missing %q", want)
		}
	}
}

func TestVerifySettings(t *testing.T) {
	off := false
	if !VerifyEnabled(nil) || !VerifyEnabled(&config.Config{}) || VerifyEnabled(&config.Config{GoalVerify: &off}) {
		t.Fatal("verifier defaults on and honors goalVerify=false")
	}
	if VerifyMaxRejections(nil) != DefaultVerifyMaxRejections || VerifyMaxRejections(&config.Config{GoalVerifyMaxRejections: 5}) != 5 {
		t.Fatal("rejection cap resolution")
	}
}
