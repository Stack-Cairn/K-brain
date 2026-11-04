package agent

import (
	"context"
	"errors"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

const (
	// maxSummaryOutputTokens caps the output budget a summary request may grow
	// to after the provider cut it off at summaryOutputTokens.
	maxSummaryOutputTokens = 32_768
	// summaryOutputGrowth multiplies the budget on each truncated attempt.
	summaryOutputGrowth = 4
)

// summaryOutputCeiling is the largest output budget a summary request may ask
// for. The session model's catalog limit bounds it; a dedicated compaction
// model's limit is unknown, so only the fixed cap applies.
func (a *Agent) summaryOutputCeiling(dedicated bool) int {
	if !dedicated && a.MaxTokens > 0 {
		return max(min(a.MaxTokens, maxSummaryOutputTokens), summaryOutputTokens)
	}
	return maxSummaryOutputTokens
}

// completeSummary sends one summary request, starting at summaryOutputTokens
// as before. When the provider stops it at the output limit (reasoning models
// spend part of max_output_tokens before writing, and long sessions need long
// summaries), it retries with a larger budget up to ceiling. A summary that is
// still cut off is never used: its last sections (the active task and the
// exact next step) are the ones lost, so the output-limit error is returned
// and the caller leaves history unchanged.
func (a *Agent) completeSummary(ctx context.Context, cli ai.Client, req ai.Request, ceiling int, usageLabel string) (string, ai.Usage, error) {
	var total ai.Usage
	budget := min(summaryOutputTokens, ceiling)
	for {
		req.MaxTokens = budget
		sum, u, err := cli.Complete(ctx, req)
		a.addModelUsage(usageLabel, u)
		addUsage(&total, u)
		if err == nil {
			return sum, total, nil
		}
		if _, truncated := errors.AsType[*ai.OutputLimitError](err); !truncated || ctx.Err() != nil || budget >= ceiling {
			return "", total, err
		}
		budget = min(budget*summaryOutputGrowth, ceiling)
	}
}
