package agent

import (
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func TestGoalVerifyExcerptIsBoundedAndKeepsTheClaim(t *testing.T) {
	msgs := []ai.Message{{Role: "system", Content: "SYSTEM PROMPT"}}
	for i := 0; i < 200; i++ {
		msgs = append(msgs, ai.Message{Role: "tool", Content: strings.Repeat("x", 600)})
	}
	msgs = append(msgs, ai.Message{Role: "user", Content: "latest ask"}, ai.Message{Role: "assistant", Content: "GOAL_MET — " + strings.Repeat("y", 10000)})
	transcript, claim := goalVerifyExcerpt(msgs)
	if len(transcript) > goalVerifyTranscriptBytes+64 {
		t.Fatalf("transcript not bounded: %d bytes", len(transcript))
	}
	if strings.Contains(transcript, "SYSTEM PROMPT") || !strings.Contains(transcript, "latest ask") {
		t.Fatal("excerpt must skip the system prompt and keep the newest work")
	}
	if !strings.HasPrefix(claim, "GOAL_MET") || len(claim) > goalVerifyClaimBytes+len("…") {
		t.Fatalf("claim: %d bytes", len(claim))
	}
}
