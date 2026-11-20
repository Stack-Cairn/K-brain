package agent

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/goalloop"
)

const (
	// GoalVerifyTimeout bounds the hidden verification request, so a slow or stuck
	// provider never holds the goal loop.
	GoalVerifyTimeout = 2 * time.Minute
	// goalVerifyMessages is how many of the latest messages the verifier sees.
	goalVerifyMessages = 40
	// goalVerifyTranscriptBytes caps the transcript excerpt; older lines go first.
	goalVerifyTranscriptBytes = 24 * 1024
	// goalVerifyClaimBytes caps the claiming reply the verifier sees in full.
	goalVerifyClaimBytes = 4000
	// goalVerifyOutputTokens leaves reasoning models room to think before the verdict.
	goalVerifyOutputTokens = 4096
)

// VerifyGoal asks the session's current model, in a hidden request that never enters
// the history, whether the goal is really met. The request carries only the goal, a
// bounded excerpt of the recent work and the reply that claimed the goal; it offers
// no tools. An error (including an unparseable verdict) leaves the decision to the
// caller, which falls back to accepting the claim.
func (a *Agent) VerifyGoal(ctx context.Context, goal string) (goalloop.Verdict, error) {
	a.clientMu.Lock()
	cli, model, label := a.Client, a.Model, a.usageLabel()
	a.clientMu.Unlock()
	if cli == nil {
		return goalloop.Verdict{}, errors.New("no model client")
	}
	transcript, claim := goalVerifyExcerpt(a.MessagesSnapshot())
	ctx, cancel := context.WithTimeout(ctx, GoalVerifyTimeout)
	defer cancel()
	reply, usage, err := cli.Complete(ctx, ai.Request{
		Model:     model,
		MaxTokens: goalVerifyOutputTokens,
		Messages: []ai.Message{
			{Role: "system", Content: goalloop.VerifyInstructions},
			{Role: "user", Content: goalloop.VerifyPrompt(goal, transcript, claim)},
		},
	})
	a.addModelUsage(label, usage)
	if err != nil {
		return goalloop.Verdict{}, err
	}
	return goalloop.ParseVerdict(reply)
}

// goalVerifyExcerpt renders the latest messages (system prompt and summaries left
// out) as a transcript capped at goalVerifyTranscriptBytes, keeping the newest lines,
// and returns the last assistant reply separately as the claim.
func goalVerifyExcerpt(messages []ai.Message) (transcript, claim string) {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" {
			if c := strings.TrimSpace(messages[i].TextContent()); c != "" {
				claim = c
				if len(claim) > goalVerifyClaimBytes {
					claim = truncateUTF8(claim, goalVerifyClaimBytes) + "…"
				}
				break
			}
		}
	}
	conv := make([]ai.Message, 0, len(messages))
	for _, m := range messages {
		if m.Role != "system" {
			conv = append(conv, m)
		}
	}
	if len(conv) > goalVerifyMessages {
		conv = conv[len(conv)-goalVerifyMessages:]
	}
	var b strings.Builder
	writeTranscript(&b, conv)
	transcript = b.String()
	if len(transcript) > goalVerifyTranscriptBytes {
		transcript = transcript[len(transcript)-goalVerifyTranscriptBytes:]
		if nl := strings.IndexByte(transcript, '\n'); nl >= 0 {
			transcript = transcript[nl+1:]
		}
		transcript = "[earlier work omitted]\n" + transcript
	}
	return strings.TrimSpace(transcript), claim
}
